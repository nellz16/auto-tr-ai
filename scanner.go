package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

func (a *App) scannerLoop(ctx context.Context) {
	for {
		a.mu.Lock()
		interval := time.Duration(
			a.cfg.ScannerIntervalSec,
		) * time.Second
		a.mu.Unlock()

		timer := time.NewTimer(interval)

		select {
		case <-ctx.Done():
			timer.Stop()
			return

		case <-timer.C:
			a.scanOnce(ctx)
		}
	}
}

func (a *App) scanOnce(parent context.Context) {
	scanContext, cfg, scanID, started := a.beginScan(parent)
	if !started {
		return
	}

	defer a.endScan(scanID)

	candidate, err := a.findBestCandidate(scanContext, cfg)
	if err != nil {
		if errors.Is(err, context.Canceled) {
			return
		}

		a.setError("scan", err)
		return
	}

	if candidate == nil {
		a.setAction("No candidate lolos filter.")
		return
	}

	if !a.scanStillValid(scanID) {
		return
	}

	if cfg.AIEnabled {
		analysis, err := a.analyzeWithGemini(
			scanContext,
			cfg,
			candidate,
			scanID,
		)

		if err != nil {
			if errors.Is(err, context.Canceled) {
				return
			}

			if errors.Is(err, ErrGeminiDeferred) {
				a.setAction(err.Error())
				return
			}

			a.setError("ai", err)
			return
		}

		candidate.AI = analysis

		if strings.ToUpper(analysis.Verdict) != "PASS" ||
			analysis.Confidence < cfg.AIMinConfidence {

			_ = a.store.SetCandidateCooldown(
				scanContext,
				candidate.Mint,
				time.Duration(
					cfg.CandidateCooldownMinutes,
				)*time.Minute,
				analysis.Reason,
			)

			a.setAction(
				fmt.Sprintf(
					"AI rejected %s: %s",
					candidate.Symbol,
					analysis.Reason,
				),
			)

			return
		}
	}

	if !a.scanStillValid(scanID) {
		return
	}

	a.mu.Lock()

	if a.paused ||
		a.position != nil ||
		a.candidate != nil ||
		a.scanSeq != scanID {

		a.mu.Unlock()
		return
	}

	a.candidate = candidate

	a.lastAction = fmt.Sprintf(
		"Candidate ready: %s score %.1f",
		candidate.Symbol,
		candidate.Score,
	)

	mode := cfg.TradingMode
	autoPaper := cfg.AllowAutobuyPaper
	autoReal := cfg.AllowAutobuyReal

	a.mu.Unlock()

	if mode == "paper" && autoPaper {
		if err := a.buyCandidate(scanContext, "paper"); err != nil {
			a.setError("paper buy", err)
		}
	}

	if mode == "real" &&
		autoReal &&
		!cfg.ApprovalRequiredReal {

		if err := a.buyCandidate(scanContext, "real"); err != nil {
			a.setError("real buy", err)
		}
	}
}

func (a *App) findBestCandidate(
	ctx context.Context,
	cfg Config,
) (*Candidate, error) {
	var profiles []DexProfile

	profilesEndpoint := strings.TrimRight(
		cfg.DexScreenerBaseURL,
		"/",
	) + "/token-profiles/latest/v1"

	if err := a.getDexJSON(
		ctx,
		profilesEndpoint,
		&profiles,
	); err != nil {
		return nil, err
	}

	mints := make([]string, 0, cfg.ScannerMaxProfiles)
	requested := make(map[string]struct{})

	for _, profile := range profiles {
		if !strings.EqualFold(profile.ChainID, "solana") {
			continue
		}

		mint := strings.TrimSpace(profile.TokenAddress)

		if mint == "" {
			continue
		}

		if _, exists := requested[mint]; exists {
			continue
		}

		coolingDown, err := a.store.IsCandidateCoolingDown(
			ctx,
			mint,
		)
		if err != nil {
			return nil, err
		}

		if coolingDown {
			continue
		}

		requested[mint] = struct{}{}
		mints = append(mints, mint)

		if len(mints) >= cfg.ScannerMaxProfiles {
			break
		}
	}

	if len(mints) == 0 {
		return nil, nil
	}

	batchEndpoint := fmt.Sprintf(
		"%s/tokens/v1/solana/%s",
		strings.TrimRight(cfg.DexScreenerBaseURL, "/"),
		strings.Join(mints, ","),
	)

	var pairs []DexPair

	if err := a.getDexJSON(
		ctx,
		batchEndpoint,
		&pairs,
	); err != nil {
		return nil, err
	}

	bestPairByMint := make(map[string]DexPair)

	for _, pair := range pairs {
		if !strings.EqualFold(pair.ChainID, "solana") {
			continue
		}

		mint := pair.BaseToken.Address

		if _, exists := requested[mint]; !exists {
			continue
		}

		current, exists := bestPairByMint[mint]

		if !exists ||
			pair.Liquidity.USD > current.Liquidity.USD {

			bestPairByMint[mint] = pair
		}
	}

	var best *Candidate

	for _, pair := range bestPairByMint {
		candidate := candidateFromPair(&pair)

		if candidate == nil {
			continue
		}

		candidate.Score = scoreCandidate(cfg, candidate)

		if !hardFilter(cfg, candidate) {
			continue
		}

		if candidate.Score < cfg.MinScore {
			continue
		}

		if best == nil || candidate.Score > best.Score {
			best = candidate
		}
	}

	return best, nil
}

// Dipakai oleh position monitor.
// Fungsi ini sengaja TIDAK memakai hard filter entry.
func (a *App) candidateFromMint(
	ctx context.Context,
	cfg Config,
	mint string,
) (*Candidate, error) {
	endpoint := fmt.Sprintf(
		"%s/token-pairs/v1/solana/%s",
		strings.TrimRight(cfg.DexScreenerBaseURL, "/"),
		url.PathEscape(mint),
	)

	var pairs []DexPair

	if err := a.getDexJSON(ctx, endpoint, &pairs); err != nil {
		return nil, err
	}

	var selected *DexPair

	for index := range pairs {
		pair := &pairs[index]

		if !strings.EqualFold(pair.ChainID, "solana") {
			continue
		}

		if pair.BaseToken.Address != mint {
			continue
		}

		if selected == nil ||
			pair.Liquidity.USD > selected.Liquidity.USD {

			selected = pair
		}
	}

	if selected == nil {
		return nil, nil
	}

	return candidateFromPair(selected), nil
}

func candidateFromPair(pair *DexPair) *Candidate {
	if pair == nil {
		return nil
	}

	price, err := strconv.ParseFloat(pair.PriceUSD, 64)
	if err != nil || price <= 0 {
		return nil
	}

	transactionCount := pair.Txns.M5.Buys +
		pair.Txns.M5.Sells

	sellRatio := 1.0

	if transactionCount > 0 {
		sellRatio = float64(pair.Txns.M5.Sells) /
			float64(transactionCount)
	}

	pairAgeMinutes := 0.0

	if pair.PairCreatedAt > 0 {
		pairAgeMinutes = time.Since(
			time.UnixMilli(pair.PairCreatedAt),
		).Minutes()
	}

	candidate := &Candidate{
		Mint:          pair.BaseToken.Address,
		Symbol:        pair.BaseToken.Symbol,
		Name:          pair.BaseToken.Name,
		PairAddress:   pair.PairAddress,
		DexID:         pair.DexID,
		URL:           pair.URL,
		PriceUSD:      price,
		LiquidityUSD:  pair.Liquidity.USD,
		Volume5mUSD:   pair.Volume.M5,
		Buys5m:        pair.Txns.M5.Buys,
		Sells5m:       pair.Txns.M5.Sells,
		Txns5m:        transactionCount,
		SellRatio5m:   sellRatio,
		PriceChange5m: pair.PriceChange.M5,
		PriceChange1h: pair.PriceChange.H1,
		PairAgeMin:    pairAgeMinutes,
	}

	rawJSON, _ := json.Marshal(pair)
	candidate.RawJSON = string(rawJSON)

	return candidate
}

func hardFilter(cfg Config, candidate *Candidate) bool {
	if candidate.LiquidityUSD < cfg.MinLiquidityUSD {
		return false
	}

	if candidate.LiquidityUSD > cfg.MaxLiquidityUSD {
		return false
	}

	if candidate.Volume5mUSD < cfg.MinVolume5mUSD {
		return false
	}

	if candidate.Txns5m < cfg.MinTxns5m {
		return false
	}

	if candidate.Buys5m < cfg.MinBuys5m {
		return false
	}

	if candidate.SellRatio5m > cfg.MaxSellRatio5m {
		return false
	}

	if candidate.PairAgeMin <
		float64(cfg.MinPairAgeMinutes) {

		return false
	}

	if candidate.PairAgeMin >
		float64(cfg.MaxPairAgeHours*60) {

		return false
	}

	if math.Abs(candidate.PriceChange5m) >
		cfg.MaxPriceChange5mPct {

		return false
	}

	if math.Abs(candidate.PriceChange1h) >
		cfg.MaxPriceChange1hPct {

		return false
	}

	return true
}

func scoreCandidate(
	cfg Config,
	candidate *Candidate,
) float64 {
	score := 0.0

	score += clamp(
		(candidate.LiquidityUSD/cfg.MinLiquidityUSD)*20,
		0,
		25,
	)

	score += clamp(
		(candidate.Volume5mUSD/cfg.MinVolume5mUSD)*20,
		0,
		25,
	)

	score += clamp(
		(float64(candidate.Txns5m)/
			float64(cfg.MinTxns5m))*15,
		0,
		20,
	)

	buyRatio := 0.0

	if candidate.Txns5m > 0 {
		buyRatio = float64(candidate.Buys5m) /
			float64(candidate.Txns5m)
	}

	score += clamp(buyRatio*20, 0, 20)

	if candidate.PriceChange5m > 0 &&
		candidate.PriceChange5m < 70 {

		score += 10
	}

	if candidate.PairAgeMin >=
		float64(cfg.MinPairAgeMinutes) &&
		candidate.PairAgeMin <= 180 {

		score += 10
	}

	return clamp(score, 0, 100)
}

func (a *App) getDexJSON(
	ctx context.Context,
	endpoint string,
	output any,
) error {
	var lastError error

	for attempt := 0; attempt < 2; attempt++ {
		if err := a.waitDex(ctx); err != nil {
			return err
		}

		request, err := http.NewRequestWithContext(
			ctx,
			http.MethodGet,
			endpoint,
			nil,
		)
		if err != nil {
			return err
		}

		response, err := a.client.Do(request)
		if err != nil {
			lastError = err

			if attempt == 0 {
				if err := sleepContext(
					ctx,
					2*time.Second,
				); err != nil {
					return err
				}

				continue
			}

			return err
		}

		body, readErr := io.ReadAll(response.Body)
		response.Body.Close()

		if readErr != nil {
			return readErr
		}

		if response.StatusCode >= 200 &&
			response.StatusCode < 300 {

			return json.Unmarshal(body, output)
		}

		apiError := newAPIError(
			"dexscreener",
			response,
			body,
		)

		lastError = apiError

		if attempt == 0 &&
			(response.StatusCode == http.StatusTooManyRequests ||
				response.StatusCode >= 500) {

			wait := apiError.RetryAfter

			if wait <= 0 {
				wait = 3 * time.Second
			}

			if err := sleepContext(ctx, wait); err != nil {
				return err
			}

			continue
		}

		return apiError
	}

	return lastError
}
