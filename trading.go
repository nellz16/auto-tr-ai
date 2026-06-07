package main

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"
)

func (a *App) buyCandidate(ctx context.Context, mode string) error {
	a.mu.Lock()
	cfg := a.cfg
	c := a.candidate
	if c == nil {
		a.mu.Unlock()
		return errors.New("no candidate")
	}
	if a.position != nil && a.position.Status == "open" {
		a.mu.Unlock()
		return errors.New("already holding")
	}
	a.mu.Unlock()

	amountUSD := cfg.MaxPositionIDR / cfg.USDIDR
	if amountUSD <= 0 {
		return errors.New("invalid amount usd")
	}

	p := &Position{
		ID:              makeID("pos"),
		Mode:            mode,
		Mint:            c.Mint,
		Symbol:          c.Symbol,
		EntryPriceUSD:   c.PriceUSD,
		LastPriceUSD:    c.PriceUSD,
		HighestPriceUSD: c.PriceUSD,
		AmountUSD:       amountUSD,
		AmountIDR:       cfg.MaxPositionIDR,
		Status:          "open",
		OpenedAt:        time.Now(),
		LastReason:      "entry",
	}

	if mode == "paper" {
		p.AmountTokenEst = amountUSD / c.PriceUSD
		p.AmountTokenRaw = ""
		p.EntryTx = "paper"
	} else {
		if a.env.PrivateKeyBase58 == "" {
			return errors.New("PRIVATE_KEY_BASE58 kosong")
		}
		if a.env.JupiterAPIKey == "" {
			return errors.New("JUPITER_API_KEY kosong; Swap V2 membutuhkan x-api-key")
		}
		lamports := int64((amountUSD / cfg.SOLUSD) * 1_000_000_000)
		if lamports <= 0 {
			return errors.New("lamports <= 0")
		}
		sig, outRaw, err := a.executeJupiterSwap(ctx, cfg, SolMint, c.Mint, strconv.FormatInt(lamports, 10))
		if err != nil {
			return err
		}
		p.EntryTx = sig
		p.AmountTokenRaw = outRaw
		p.AmountTokenEst = 0
	}

	if err := a.store.SavePosition(ctx, p); err != nil {
		return err
	}
	a.store.SaveTrade(ctx, p, "buy", p.EntryTx, "entry", p.AmountTokenRaw, p.AmountTokenEst, p.AmountUSD)
	a.store.SaveEvent(ctx, "info", "buy", fmt.Sprintf("Bought %s in %s", p.Symbol, mode), p)

	a.mu.Lock()
	a.position = p
	a.lastAction = fmt.Sprintf("BUY %s %s amount Rp%.0f", strings.ToUpper(mode), p.Symbol, p.AmountIDR)
	a.candidate = nil
	a.mu.Unlock()

	return nil
}

func (a *App) sellCurrent(ctx context.Context, reason string) error {
	a.mu.Lock()
	cfg := a.cfg
	p := a.position
	if p == nil || p.Status != "open" {
		a.mu.Unlock()
		return errors.New("no open position")
	}
	a.mu.Unlock()

	exitTx := "paper"
	if p.Mode == "real" {
		if a.env.PrivateKeyBase58 == "" {
			return errors.New("PRIVATE_KEY_BASE58 kosong")
		}
		if a.env.JupiterAPIKey == "" {
			return errors.New("JUPITER_API_KEY kosong")
		}
		amountRaw := p.AmountTokenRaw
		if amountRaw == "" || amountRaw == "0" {
			return errors.New("amount token raw kosong; tidak bisa sell real otomatis")
		}
		sig, _, err := a.executeJupiterSwap(ctx, cfg, p.Mint, SolMint, amountRaw)
		if err != nil {
			return err
		}
		exitTx = sig
	}

	nowTime := time.Now()
	p.Status = "closed"
	p.ClosedAt = &nowTime
	p.ExitTx = exitTx
	p.LastReason = reason

	if err := a.store.SavePosition(ctx, p); err != nil {
		return err
	}
	a.store.SaveTrade(ctx, p, "sell", exitTx, reason, p.AmountTokenRaw, p.AmountTokenEst, p.AmountUSD)
	a.store.SaveEvent(ctx, "info", "sell", fmt.Sprintf("Sold %s: %s", p.Symbol, reason), p)

	a.mu.Lock()
	a.position = nil
	a.lastAction = fmt.Sprintf("SELL %s: %s", p.Symbol, reason)
	a.mu.Unlock()

	return nil
}

func (a *App) positionLoop(ctx context.Context) {
	for {
		a.mu.Lock()
		interval := time.Duration(a.cfg.PositionIntervalSec) * time.Second
		a.mu.Unlock()

		select {
		case <-ctx.Done():
			return
		case <-time.After(interval):
			a.monitorPosition(ctx)
		}
	}
}

func (a *App) monitorPosition(ctx context.Context) {
	a.mu.Lock()
	p := a.position
	cfg := a.cfg
	a.mu.Unlock()

	if p == nil || p.Status != "open" {
		return
	}

	c, err := a.candidateFromMint(ctx, cfg, p.Mint)
	if err != nil || c == nil || c.PriceUSD <= 0 {
		return
	}

	p.LastPriceUSD = c.PriceUSD
	if p.LastPriceUSD > p.HighestPriceUSD {
		p.HighestPriceUSD = p.LastPriceUSD
	}

	pnl := ((p.LastPriceUSD - p.EntryPriceUSD) / p.EntryPriceUSD) * 100
	drawdownFromHigh := ((p.LastPriceUSD - p.HighestPriceUSD) / p.HighestPriceUSD) * 100
	holdMin := time.Since(p.OpenedAt).Minutes()

	reason := ""

	if pnl <= cfg.StopLossPct {
		reason = fmt.Sprintf("stop loss %.2f%%", pnl)
	} else if holdMin >= float64(cfg.MaxHoldMinutes) {
		reason = fmt.Sprintf("max hold %.0f min", holdMin)
	} else if pnl >= cfg.TakeProfit2Pct {
		reason = fmt.Sprintf("take profit 2 %.2f%%", pnl)
	} else if pnl >= cfg.TrailingStartPct && drawdownFromHigh <= -math.Abs(cfg.TrailingDistancePct) {
		reason = fmt.Sprintf("trailing stop pnl %.2f%% dd %.2f%%", pnl, drawdownFromHigh)
	} else if pnl >= cfg.TakeProfit1Pct && !p.TP1Done {
		p.TP1Done = true
		p.LastReason = fmt.Sprintf("TP1 touched %.2f%%; full exit still protected by TP2/trailing", pnl)
	}

	_ = a.store.SavePosition(ctx, p)

	a.mu.Lock()
	a.position = p
	a.lastAction = fmt.Sprintf("Holding %s | PnL %.2f%% | high %.8f", p.Symbol, pnl, p.HighestPriceUSD)
	a.mu.Unlock()

	if reason != "" {
		if err := a.sellCurrent(ctx, reason); err != nil {
			a.setError("sell", err)
		}
	}
}
