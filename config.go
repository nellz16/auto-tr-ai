package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
)

func loadEnv() (Env, error) {
	port := getenv("PORT", "8080")

	ownerID, err := strconv.ParseInt(strings.TrimSpace(os.Getenv("TELEGRAM_OWNER_ID")), 10, 64)
	if err != nil || ownerID == 0 {
		return Env{}, errors.New("TELEGRAM_OWNER_ID wajib diisi angka Telegram user ID kamu")
	}

	env := Env{
		Port:             port,
		TelegramBotToken: strings.TrimSpace(os.Getenv("TELEGRAM_BOT_TOKEN")),
		TelegramOwnerID:  ownerID,
		TursoDatabaseURL: strings.TrimSpace(os.Getenv("TURSO_DATABASE_URL")),
		TursoAuthToken:   strings.TrimSpace(os.Getenv("TURSO_AUTH_TOKEN")),
		GeminiAPIKey:     strings.TrimSpace(os.Getenv("GEMINI_API_KEY")),
		HeliusRPCURL:     strings.TrimSpace(os.Getenv("HELIUS_RPC_URL")),
		JupiterAPIKey:    strings.TrimSpace(os.Getenv("JUPITER_API_KEY")),
		PrivateKeyBase58: strings.TrimSpace(os.Getenv("PRIVATE_KEY_BASE58")),
	}

	required := map[string]string{
		"TELEGRAM_BOT_TOKEN": env.TelegramBotToken,
		"TURSO_DATABASE_URL": env.TursoDatabaseURL,
		"TURSO_AUTH_TOKEN":   env.TursoAuthToken,
		"GEMINI_API_KEY":     env.GeminiAPIKey,
		"HELIUS_RPC_URL":     env.HeliusRPCURL,
	}

	for k, v := range required {
		if strings.TrimSpace(v) == "" {
			return Env{}, fmt.Errorf("%s wajib diisi", k)
		}
	}

	return env, nil
}

func getenv(key, fallback string) string {
	v := strings.TrimSpace(os.Getenv(key))
	if v == "" {
		return fallback
	}
	return v
}

func (a *App) reloadConfig(ctx context.Context) error {
	settings, err := a.store.GetAllSettings(ctx)
	if err != nil {
		return err
	}

	cfg := Config{
		TradingMode:          strSetting(settings, "trading.mode", "paper"),
		AllowAutobuyPaper:    boolSetting(settings, "trading.allow_autobuy_paper", true),
		AllowAutobuyReal:     boolSetting(settings, "trading.allow_autobuy_real", false),
		ApprovalRequiredReal: boolSetting(settings, "trading.approval_required_real", true),
		MaxOpenPositions:     intSetting(settings, "risk.max_open_positions", 1),
		MaxPositionIDR:       floatSetting(settings, "risk.max_position_idr", 10000),
		MaxDailyLossIDR:      floatSetting(settings, "risk.max_daily_loss_idr", 15000),
		MaxDailyTrades:       intSetting(settings, "risk.max_daily_trades", 3),
		StopLossPct:          floatSetting(settings, "exit.stop_loss_pct", -20),
		TakeProfit1Pct:       floatSetting(settings, "exit.take_profit_1_pct", 35),
		TakeProfit1SellPct:   floatSetting(settings, "exit.take_profit_1_sell_pct", 50),
		TakeProfit2Pct:       floatSetting(settings, "exit.take_profit_2_pct", 70),
		TrailingStartPct:     floatSetting(settings, "exit.trailing_start_pct", 35),
		TrailingDistancePct:  floatSetting(settings, "exit.trailing_distance_pct", 18),
		MaxHoldMinutes:       intSetting(settings, "exit.max_hold_minutes", 45),
		SlippageBps:          intSetting(settings, "swap.slippage_bps", 700),
		ScannerIntervalSec:   intSetting(settings, "interval.scanner_seconds", 35),
		PositionIntervalSec:  intSetting(settings, "interval.position_seconds", 10),
		TelegramEditInterval: intSetting(settings, "interval.telegram_edit_seconds", 6),
		USDIDR:               floatSetting(settings, "market.usd_idr", 16500),
		SOLUSD:               floatSetting(settings, "market.sol_usd", 150),
		ScannerMaxProfiles:   intSetting(settings, "scanner.max_profiles", 25),
		MinLiquidityUSD:      floatSetting(settings, "scanner.min_liquidity_usd", 8000),
		MaxLiquidityUSD:      floatSetting(settings, "scanner.max_liquidity_usd", 300000),
		MinVolume5mUSD:       floatSetting(settings, "scanner.min_volume_5m_usd", 3000),
		MinTxns5m:            intSetting(settings, "scanner.min_txns_5m", 30),
		MinBuys5m:            intSetting(settings, "scanner.min_buys_5m", 18),
		MaxSellRatio5m:       floatSetting(settings, "scanner.max_sell_ratio_5m", 0.55),
		MinPairAgeMinutes:    intSetting(settings, "scanner.min_pair_age_minutes", 10),
		MaxPairAgeHours:      intSetting(settings, "scanner.max_pair_age_hours", 12),
		MaxPriceChange5mPct:  floatSetting(settings, "scanner.max_price_change_5m_pct", 90),
		MaxPriceChange1hPct:  floatSetting(settings, "scanner.max_price_change_1h_pct", 260),
		MinScore:             floatSetting(settings, "scanner.min_score", 70),
		AIEnabled:            boolSetting(settings, "ai.enabled", true),
		AIMinConfidence:      floatSetting(settings, "ai.min_confidence", 0.70),
		AIModelPriority:      csvSetting(settings, "ai.model_priority"),
		AIModelCooldown:      intSetting(settings, "ai.model_cooldown_minutes", 30),
		DexScreenerBaseURL:   strSetting(settings, "api.dexscreener_base_url", DefaultDexScreenerBase),
		GeminiBaseURL:        strSetting(settings, "api.gemini_base_url", DefaultGeminiBaseURL),
		JupiterBaseURL:       strSetting(settings, "api.jupiter_base_url", DefaultJupiterBaseURL),
	}

	if len(cfg.AIModelPriority) == 0 {
		cfg.AIModelPriority = []string{"gemini-2.5-flash-lite"}
	}

	if cfg.ScannerIntervalSec < 20 {
		cfg.ScannerIntervalSec = 20
	}
	if cfg.PositionIntervalSec < 5 {
		cfg.PositionIntervalSec = 5
	}

	a.mu.Lock()
	a.cfg = cfg
	a.mu.Unlock()

	return nil
}
