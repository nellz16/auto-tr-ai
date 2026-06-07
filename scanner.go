package main

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/url"
	"strconv"
	"strings"
	"time"
)

func (a *App) scannerLoop(ctx context.Context) {
	for {
		a.mu.Lock()
		interval := time.Duration(a.cfg.ScannerIntervalSec) * time.Second
		a.mu.Unlock()

		select {
		case <-ctx.Done():
			return
		case <-time.After(interval):
			a.scanOnce(ctx)
		}
	}
}

func (a *App) scanOnce(ctx context.Context) {
	a.mu.Lock()
	if a.paused {
		a.lastAction = "Scanner paused."
		a.mu.Unlock()
		return
	}
	if a.position != nil && a.position.Status == "open" {
		a.lastAction = "Skip scan: masih holding 1 token."
		a.mu.Unlock()
		return
	}
	cfg := a.cfg
	a.lastScanAt = time.Now()
	a.mu.Unlock()

	cand, err := a.findBestCandidate(ctx, cfg)
	if err != nil {
		a.setError("scan", err)
		return
	}
	if cand == nil {
		a.setAction("No candidate lolos filter.")
		return
	}

	if cfg.AIEnabled {
		ai, err := a.analyzeWithGemini(ctx, cfg, cand)
		if err != nil {
			a.setError("ai", err)
			return
		}
		cand.AI = ai
		if strings.ToUpper(ai.Verdict) != "PASS" || ai.Confidence < cfg.AIMinConfidence {
			a.setAction(fmt.Sprintf("AI rejected %s: %s", cand.Symbol, ai.Reason))
			return
		}
	}

	a.mu.Lock()
	a.candidate = cand
	a.lastAction = fmt.Sprintf("Candidate ready: %s score %.1f", cand.Symbol, cand.Score)
	mode := cfg.TradingMode
	autoPaper := cfg.AllowAutobuyPaper
	autoReal := cfg.AllowAutobuyReal
	a.mu.Unlock()

	if mode == "paper" && autoPaper {
		_ = a.buyCandidate(ctx, "paper")
	}
	if mode == "real" && autoReal && !cfg.ApprovalRequiredReal {
		_ = a.buyCandidate(ctx, "real")
	}
}

func (a *App) findBestCandidate(ctx context.Context, cfg Config) (*Candidate, error) {
	var profiles []DexProfile
	if err := a.getJSON(ctx, cfg.DexScreenerBaseURL+"/token-profiles/latest/v1", nil, &profiles); err != nil {
		return nil, err
	}

	best := (*Candidate)(nil)
	checked := 0

	for _, p := range profiles {
		if strings.ToLower(p.ChainID) != "solana" || p.TokenAddress == "" {
			continue
		}
		checked++
		if checked > cfg.ScannerMaxProfiles {
			break
		}

		c, err := a.candidateFromMint(ctx, cfg, p.TokenAddress)
		if err != nil || c == nil {
			continue
		}
		if best == nil || c.Score > best.Score {
			best = c
		}
	}

	return best, nil
}

func (a *App) candidateFromMint(ctx context.Context, cfg Config, mint string) (*Candidate, error) {
	endpoint := fmt.Sprintf("%s/latest/dex/tokens/%s", cfg.DexScreenerBaseURL, url.PathEscape(mint))

	var resp DexTokenPairsResponse
	if err := a.getJSON(ctx, endpoint, nil, &resp); err != nil {
		return nil, err
	}
	if len(resp.Pairs) == 0 {
		return nil, nil
	}

	var chosen *DexPair
	for i := range resp.Pairs {
		p := &resp.Pairs[i]
		if strings.ToLower(p.ChainID) != "solana" {
			continue
		}
		if chosen == nil || p.Liquidity.USD > chosen.Liquidity.USD {
			chosen = p
		}
	}
	if chosen == nil {
		return nil, nil
	}

	price, _ := strconv.ParseFloat(chosen.PriceUSD, 64)
	if price <= 0 {
		return nil, nil
	}

	txns := chosen.Txns.M5.Buys + chosen.Txns.M5.Sells
	sellRatio := 1.0
	if txns > 0 {
		sellRatio = float64(chosen.Txns.M5.Sells) / float64(txns)
	}

	ageMin := 0.0
	if chosen.PairCreatedAt > 0 {
		ageMin = time.Since(time.UnixMilli(chosen.PairCreatedAt)).Minutes()
	}

	c := &Candidate{
		Mint:          chosen.BaseToken.Address,
		Symbol:        chosen.BaseToken.Symbol,
		Name:          chosen.BaseToken.Name,
		PairAddress:   chosen.PairAddress,
		DexID:         chosen.DexID,
		URL:           chosen.URL,
		PriceUSD:      price,
		LiquidityUSD:  chosen.Liquidity.USD,
		Volume5mUSD:   chosen.Volume.M5,
		Buys5m:        chosen.Txns.M5.Buys,
		Sells5m:       chosen.Txns.M5.Sells,
		Txns5m:        txns,
		SellRatio5m:   sellRatio,
		PriceChange5m: chosen.PriceChange.M5,
		PriceChange1h: chosen.PriceChange.H1,
		PairAgeMin:    ageMin,
	}

	raw, _ := json.Marshal(chosen)
	c.RawJSON = string(raw)
	c.Score = scoreCandidate(cfg, c)

	if !hardFilter(cfg, c) {
		return nil, nil
	}
	if c.Score < cfg.MinScore {
		return nil, nil
	}
	return c, nil
}

func hardFilter(cfg Config, c *Candidate) bool {
	if c.LiquidityUSD < cfg.MinLiquidityUSD {
		return false
	}
	if c.LiquidityUSD > cfg.MaxLiquidityUSD {
		return false
	}
	if c.Volume5mUSD < cfg.MinVolume5mUSD {
		return false
	}
	if c.Txns5m < cfg.MinTxns5m {
		return false
	}
	if c.Buys5m < cfg.MinBuys5m {
		return false
	}
	if c.SellRatio5m > cfg.MaxSellRatio5m {
		return false
	}
	if c.PairAgeMin < float64(cfg.MinPairAgeMinutes) {
		return false
	}
	if c.PairAgeMin > float64(cfg.MaxPairAgeHours*60) {
		return false
	}
	if math.Abs(c.PriceChange5m) > cfg.MaxPriceChange5mPct {
		return false
	}
	if math.Abs(c.PriceChange1h) > cfg.MaxPriceChange1hPct {
		return false
	}
	return true
}

func scoreCandidate(cfg Config, c *Candidate) float64 {
	score := 0.0

	score += clamp((c.LiquidityUSD/cfg.MinLiquidityUSD)*20, 0, 25)
	score += clamp((c.Volume5mUSD/cfg.MinVolume5mUSD)*20, 0, 25)
	score += clamp((float64(c.Txns5m)/float64(cfg.MinTxns5m))*15, 0, 20)

	buyRatio := 0.0
	if c.Txns5m > 0 {
		buyRatio = float64(c.Buys5m) / float64(c.Txns5m)
	}
	score += clamp(buyRatio*20, 0, 20)

	if c.PriceChange5m > 0 && c.PriceChange5m < 70 {
		score += 10
	}
	if c.PairAgeMin >= float64(cfg.MinPairAgeMinutes) && c.PairAgeMin <= 180 {
		score += 10
	}
	return clamp(score, 0, 100)
}
