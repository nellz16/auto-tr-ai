package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

func loadEnv() (Env, error) {
	port := getenv("PORT", "8080")

	ownerID, err := strconv.ParseInt(
		strings.TrimSpace(os.Getenv("TELEGRAM_OWNER_ID")),
		10,
		64,
	)
	if err != nil || ownerID == 0 {
		return Env{}, errors.New(
			"TELEGRAM_OWNER_ID wajib diisi angka Telegram user ID kamu",
		)
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

	for key, value := range required {
		if strings.TrimSpace(value) == "" {
			return Env{}, fmt.Errorf("%s wajib diisi", key)
		}
	}

	return env, nil
}

func getenv(key string, fallback string) string {
	value := strings.TrimSpace(os.Getenv(key))

	if value == "" {
		return fallback
	}

	return value
}

func (a *App) reloadConfig(ctx context.Context) error {
	settings, err := a.store.GetAllSettings(ctx)
	if err != nil {
		return err
	}

	cfg := Config{
		TradingMode: strSetting(
			settings,
			"trading.mode",
			"paper",
		),

		AllowAutobuyPaper: boolSetting(
			settings,
			"trading.allow_autobuy_paper",
			true,
		),

		AllowAutobuyReal: boolSetting(
			settings,
			"trading.allow_autobuy_real",
			false,
		),

		ApprovalRequiredReal: boolSetting(
			settings,
			"trading.approval_required_real",
			true,
		),

		MaxOpenPositions: intSetting(
			settings,
			"risk.max_open_positions",
			1,
		),

		MaxPositionIDR: floatSetting(
			settings,
			"risk.max_position_idr",
			10000,
		),

		MaxDailyLossIDR: floatSetting(
			settings,
			"risk.max_daily_loss_idr",
			15000,
		),

		MaxDailyTrades: intSetting(
			settings,
			"risk.max_daily_trades",
			3,
		),

		StopLossPct: floatSetting(
			settings,
			"exit.stop_loss_pct",
			-20,
		),

		TakeProfit1Pct: floatSetting(
			settings,
			"exit.take_profit_1_pct",
			35,
		),

		TakeProfit1SellPct: floatSetting(
			settings,
			"exit.take_profit_1_sell_pct",
			50,
		),

		TakeProfit2Pct: floatSetting(
			settings,
			"exit.take_profit_2_pct",
			70,
		),

		TrailingStartPct: floatSetting(
			settings,
			"exit.trailing_start_pct",
			35,
		),

		TrailingDistancePct: floatSetting(
			settings,
			"exit.trailing_distance_pct",
			18,
		),

		MaxHoldMinutes: intSetting(
			settings,
			"exit.max_hold_minutes",
			45,
		),

		SlippageBps: intSetting(
			settings,
			"swap.slippage_bps",
			700,
		),

		ScannerIntervalSec: intSetting(
			settings,
			"interval.scanner_seconds",
			35,
		),

		PositionIntervalSec: intSetting(
			settings,
			"interval.position_seconds",
			10,
		),

		TelegramEditInterval: intSetting(
			settings,
			"interval.telegram_edit_seconds",
			15,
		),

		USDIDR: floatSetting(
			settings,
			"market.usd_idr",
			16500,
		),

		SOLUSD: floatSetting(
			settings,
			"market.sol_usd",
			150,
		),

		ScannerMaxProfiles: intSetting(
			settings,
			"scanner.max_profiles",
			25,
		),

		MinLiquidityUSD: floatSetting(
			settings,
			"scanner.min_liquidity_usd",
			8000,
		),

		MaxLiquidityUSD: floatSetting(
			settings,
			"scanner.max_liquidity_usd",
			300000,
		),

		MinVolume5mUSD: floatSetting(
			settings,
			"scanner.min_volume_5m_usd",
			3000,
		),

		MinTxns5m: intSetting(
			settings,
			"scanner.min_txns_5m",
			30,
		),

		MinBuys5m: intSetting(
			settings,
			"scanner.min_buys_5m",
			18,
		),

		MaxSellRatio5m: floatSetting(
			settings,
			"scanner.max_sell_ratio_5m",
			0.55,
		),

		MinPairAgeMinutes: intSetting(
			settings,
			"scanner.min_pair_age_minutes",
			10,
		),

		MaxPairAgeHours: intSetting(
			settings,
			"scanner.max_pair_age_hours",
			12,
		),

		MaxPriceChange5mPct: floatSetting(
			settings,
			"scanner.max_price_change_5m_pct",
			90,
		),

		MaxPriceChange1hPct: floatSetting(
			settings,
			"scanner.max_price_change_1h_pct",
			260,
		),

		MinScore: floatSetting(
			settings,
			"scanner.min_score",
			70,
		),

		CandidateCooldownMinutes: intSetting(
			settings,
			"scanner.candidate_cooldown_minutes",
			30,
		),

		AIEnabled: boolSetting(
			settings,
			"ai.enabled",
			true,
		),

		AIMinConfidence: floatSetting(
			settings,
			"ai.min_confidence",
			0.70,
		),

		AIModelPriority: csvSetting(
			settings,
			"ai.model_priority",
		),

		AIModelCooldownMinutes: intSetting(
			settings,
			"ai.model_cooldown_minutes",
			30,
		),

		AIMinIntervalSeconds: intSetting(
			settings,
			"ai.min_interval_seconds",
			120,
		),

		AIDailyMaxCalls: intSetting(
			settings,
			"ai.daily_max_calls",
			25,
		),

		AICacheTTLMinutes: intSetting(
			settings,
			"ai.cache_ttl_minutes",
			30,
		),

		AIMaxAttemptsPerAnalysis: intSetting(
			settings,
			"ai.max_attempts_per_analysis",
			2,
		),

		AIRateLimitCooldownMinutes: intSetting(
			settings,
			"ai.rate_limit_cooldown_minutes",
			60,
		),

		DexMinIntervalMS: intSetting(
			settings,
			"api.dex_min_interval_ms",
			1100,
		),

		GeminiMinIntervalMS: intSetting(
			settings,
			"api.gemini_min_interval_ms",
			1100,
		),

		JupiterMinIntervalMS: intSetting(
			settings,
			"api.jupiter_min_interval_ms",
			1100,
		),

		DexScreenerBaseURL: strSetting(
			settings,
			"api.dexscreener_base_url",
			DefaultDexScreenerBase,
		),

		GeminiBaseURL: strSetting(
			settings,
			"api.gemini_base_url",
			DefaultGeminiBaseURL,
		),

		JupiterBaseURL: strSetting(
			settings,
			"api.jupiter_base_url",
			DefaultJupiterBaseURL,
		),
	}

	if len(cfg.AIModelPriority) == 0 {
		cfg.AIModelPriority = []string{
			"gemini-3.5-flash",
			"gemini-3.1-flash-lite",
			"gemini-3-flash-preview",
			"gemini-2.5-flash",
			"gemini-2.5-flash-lite",
		}
	}

	if cfg.ScannerIntervalSec < 30 {
		cfg.ScannerIntervalSec = 30
	}

	if cfg.PositionIntervalSec < 10 {
		cfg.PositionIntervalSec = 10
	}

	if cfg.TelegramEditInterval < 15 {
		cfg.TelegramEditInterval = 15
	}

	if cfg.ScannerMaxProfiles < 1 {
		cfg.ScannerMaxProfiles = 1
	}

	if cfg.ScannerMaxProfiles > 30 {
		cfg.ScannerMaxProfiles = 30
	}

	if cfg.CandidateCooldownMinutes < 5 {
		cfg.CandidateCooldownMinutes = 5
	}

	if cfg.AIMinIntervalSeconds < 30 {
		cfg.AIMinIntervalSeconds = 30
	}

	if cfg.AIDailyMaxCalls < 1 {
		cfg.AIDailyMaxCalls = 1
	}

	if cfg.AICacheTTLMinutes < 5 {
		cfg.AICacheTTLMinutes = 5
	}

	if cfg.AIMaxAttemptsPerAnalysis < 1 {
		cfg.AIMaxAttemptsPerAnalysis = 1
	}

	if cfg.AIMaxAttemptsPerAnalysis > len(cfg.AIModelPriority) {
		cfg.AIMaxAttemptsPerAnalysis = len(cfg.AIModelPriority)
	}

	if cfg.AIRateLimitCooldownMinutes < 15 {
		cfg.AIRateLimitCooldownMinutes = 15
	}

	if cfg.DexMinIntervalMS < 1000 {
		cfg.DexMinIntervalMS = 1000
	}

	if cfg.GeminiMinIntervalMS < 1000 {
		cfg.GeminiMinIntervalMS = 1000
	}

	if cfg.JupiterMinIntervalMS < 1100 {
		cfg.JupiterMinIntervalMS = 1100
	}

	a.mu.Lock()

	a.cfg = cfg

	a.dexLimiter = NewIntervalLimiter(
		time.Duration(cfg.DexMinIntervalMS) * time.Millisecond,
	)

	a.geminiLimiter = NewIntervalLimiter(
		time.Duration(cfg.GeminiMinIntervalMS) * time.Millisecond,
	)

	a.jupiterLimiter = NewIntervalLimiter(
		time.Duration(cfg.JupiterMinIntervalMS) * time.Millisecond,
	)

	if a.modelCooldown == nil {
		a.modelCooldown = make(map[string]time.Time)
	}

	a.mu.Unlock()

	return nil
}
