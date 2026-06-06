package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"math"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	bin "github.com/gagliardetto/binary"
	"github.com/gagliardetto/solana-go"
	turso "turso.tech/database/tursogo"
)

const (
	AppName = "auto-tr-ai"

	SolMint = "So11111111111111111111111111111111111111112"

	DefaultJupiterBaseURL    = "https://api.jup.ag/swap/v2"
	DefaultDexScreenerBase   = "https://api.dexscreener.com"
	DefaultGeminiBaseURL     = "https://generativelanguage.googleapis.com/v1beta"
	DefaultLocalDBPath       = "/tmp/auto-tr-ai.db"
	DefaultDashboardTextHash = "-"
)

type Env struct {
	Port             string
	TelegramBotToken string
	TelegramOwnerID  int64

	TursoDatabaseURL string
	TursoAuthToken   string

	GeminiAPIKey string
	HeliusRPCURL string
	JupiterAPIKey string

	PrivateKeyBase58 string
}

type App struct {
	env    Env
	store  *Store
	client *http.Client

	mu          sync.Mutex
	cfg         Config
	paused      bool
	lastScanAt   time.Time
	lastAction   string
	lastError    string
	candidate   *Candidate
	position    *Position
	updateOffset int64
}

type Store struct {
	db     *sql.DB
	syncDB *turso.TursoSyncDb
}

type Config struct {
	TradingMode string

	AllowAutobuyPaper   bool
	AllowAutobuyReal    bool
	ApprovalRequiredReal bool

	MaxOpenPositions int
	MaxPositionIDR   float64
	MaxDailyLossIDR  float64
	MaxDailyTrades   int

	StopLossPct          float64
	TakeProfit1Pct       float64
	TakeProfit1SellPct   float64
	TakeProfit2Pct       float64
	TrailingStartPct     float64
	TrailingDistancePct  float64
	MaxHoldMinutes       int
	SlippageBps          int
	ScannerIntervalSec   int
	PositionIntervalSec  int
	TelegramEditInterval int

	USDIDR float64
	SOLUSD float64

	ScannerMaxProfiles       int
	MinLiquidityUSD         float64
	MaxLiquidityUSD         float64
	MinVolume5mUSD          float64
	MinTxns5m               int
	MinBuys5m               int
	MaxSellRatio5m          float64
	MinPairAgeMinutes       int
	MaxPairAgeHours         int
	MaxPriceChange5mPct     float64
	MaxPriceChange1hPct     float64
	MinScore                float64

	AIEnabled       bool
	AIMinConfidence float64
	AIModelPriority []string
	AIModelCooldown int

	DexScreenerBaseURL string
	GeminiBaseURL      string
	JupiterBaseURL     string
}

type Candidate struct {
	Mint          string
	Symbol        string
	Name          string
	PairAddress   string
	DexID         string
	URL           string
	PriceUSD      float64
	LiquidityUSD  float64
	Volume5mUSD   float64
	Buys5m        int
	Sells5m       int
	Txns5m        int
	SellRatio5m   float64
	PriceChange5m float64
	PriceChange1h float64
	PairAgeMin    float64
	Score         float64
	AI            *AIResult
	RawJSON       string
}

type Position struct {
	ID             string
	Mode           string
	Mint           string
	Symbol         string
	EntryPriceUSD  float64
	LastPriceUSD   float64
	HighestPriceUSD float64
	AmountTokenRaw string
	AmountTokenEst float64
	AmountUSD      float64
	AmountIDR      float64
	Status         string
	TP1Done        bool
	TP2Done        bool
	EntryTx        string
	ExitTx         string
	OpenedAt       time.Time
	ClosedAt       *time.Time
	LastReason     string
}

type AIResult struct {
	Verdict        string  `json:"verdict"`
	Confidence    float64 `json:"confidence"`
	Risk           string  `json:"risk"`
	Reason         string  `json:"reason"`
	MaxHoldMinutes int     `json:"max_hold_minutes"`
	ModelUsed      string  `json:"model_used,omitempty"`
}

type TelegramUpdateResponse struct {
	OK     bool             `json:"ok"`
	Result []TelegramUpdate `json:"result"`
}

type TelegramUpdate struct {
	UpdateID      int64             `json:"update_id"`
	Message       *TelegramMessage  `json:"message"`
	CallbackQuery *TelegramCallback `json:"callback_query"`
}

type TelegramMessage struct {
	MessageID int64        `json:"message_id"`
	From      TelegramUser `json:"from"`
	Chat      TelegramChat `json:"chat"`
	Text      string       `json:"text"`
}

type TelegramCallback struct {
	ID      string          `json:"id"`
	From    TelegramUser    `json:"from"`
	Message TelegramMessage `json:"message"`
	Data    string          `json:"data"`
}

type TelegramUser struct {
	ID        int64  `json:"id"`
	Username  string `json:"username"`
	FirstName string `json:"first_name"`
}

type TelegramChat struct {
	ID int64 `json:"id"`
}

type InlineKeyboardMarkup struct {
	InlineKeyboard [][]InlineKeyboardButton `json:"inline_keyboard"`
}

type InlineKeyboardButton struct {
	Text         string `json:"text"`
	CallbackData string `json:"callback_data"`
}

type DexProfile struct {
	ChainID      string `json:"chainId"`
	TokenAddress string `json:"tokenAddress"`
	URL          string `json:"url"`
	Description  string `json:"description"`
}

type DexTokenPairsResponse struct {
	Pairs []DexPair `json:"pairs"`
}

type DexPair struct {
	ChainID     string `json:"chainId"`
	DexID       string `json:"dexId"`
	URL         string `json:"url"`
	PairAddress string `json:"pairAddress"`
	BaseToken   struct {
		Address string `json:"address"`
		Name    string `json:"name"`
		Symbol  string `json:"symbol"`
	} `json:"baseToken"`
	QuoteToken struct {
		Address string `json:"address"`
		Name    string `json:"name"`
		Symbol  string `json:"symbol"`
	} `json:"quoteToken"`
	PriceNative string `json:"priceNative"`
	PriceUSD    string `json:"priceUsd"`
	Txns         struct {
		M5 struct {
			Buys  int `json:"buys"`
			Sells int `json:"sells"`
		} `json:"m5"`
		H1 struct {
			Buys  int `json:"buys"`
			Sells int `json:"sells"`
		} `json:"h1"`
	} `json:"txns"`
	Volume struct {
		M5 float64 `json:"m5"`
		H1 float64 `json:"h1"`
	} `json:"volume"`
	PriceChange struct {
		M5 float64 `json:"m5"`
		H1 float64 `json:"h1"`
	} `json:"priceChange"`
	Liquidity struct {
		USD   float64 `json:"usd"`
		Base  float64 `json:"base"`
		Quote float64 `json:"quote"`
	} `json:"liquidity"`
	PairCreatedAt int64 `json:"pairCreatedAt"`
}

type GeminiRequest struct {
	SystemInstruction *GeminiSystemInstruction `json:"system_instruction,omitempty"`
	Contents         []GeminiContent          `json:"contents"`
	GenerationConfig GeminiGenerationConfig   `json:"generationConfig"`
}

type GeminiSystemInstruction struct {
	Parts []GeminiPart `json:"parts"`
}

type GeminiContent struct {
	Parts []GeminiPart `json:"parts"`
}

type GeminiPart struct {
	Text string `json:"text"`
}

type GeminiGenerationConfig struct {
	ResponseMIMEType string  `json:"response_mime_type"`
	Temperature      float64 `json:"temperature"`
}

type GeminiResponse struct {
	Candidates []struct {
		Content struct {
			Parts []GeminiPart `json:"parts"`
		} `json:"content"`
	} `json:"candidates"`
	Error *struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
		Status  string `json:"status"`
	} `json:"error,omitempty"`
}

type JupiterOrderResponse struct {
	Transaction             string `json:"transaction"`
	RequestID               string `json:"requestId"`
	InAmount                string `json:"inAmount"`
	OutAmount               string `json:"outAmount"`
	PriceImpact             string `json:"priceImpact"`
	PriceImpactPct          string `json:"priceImpactPct"`
	SignatureFeeLamports    int64  `json:"signatureFeeLamports"`
	PrioritizationFeeLamports int64 `json:"prioritizationFeeLamports"`
	TotalTime               int64  `json:"totalTime"`
	Error                   string `json:"error"`
	Message                 string `json:"message"`
}

type JupiterExecuteResponse struct {
	Status    string `json:"status"`
	Signature string `json:"signature"`
	Slot      int64  `json:"slot"`
	Code      int    `json:"code"`
	Error     string `json:"error"`
	Message   string `json:"message"`
}

func main() {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	env, err := loadEnv()
	if err != nil {
		log.Fatal(err)
	}

	store, err := OpenStore(ctx, env)
	if err != nil {
		log.Fatal(err)
	}
	defer store.Close()

	app := &App{
		env:    env,
		store:  store,
		client: &http.Client{Timeout: 25 * time.Second},
		paused: false,
		lastAction: "Booting...",
	}

	if err := app.reloadConfig(ctx); err != nil {
		log.Fatal(err)
	}

	pos, err := store.LoadOpenPosition(ctx)
	if err == nil && pos != nil {
		app.position = pos
	}

	go app.telegramPoller(ctx)
	go app.scannerLoop(ctx)
	go app.positionLoop(ctx)
	go app.dashboardLoop(ctx)

	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", app.handleHealthz)
	mux.HandleFunc("/", app.handleRoot)

	server := &http.Server{
		Addr:              ":" + env.Port,
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
	}

	log.Printf("%s started on :%s", AppName, env.Port)
	if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Fatal(err)
	}
}

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

func OpenStore(ctx context.Context, env Env) (*Store, error) {
	syncDB, err := turso.NewTursoSyncDb(ctx, turso.TursoSyncDbConfig{
		Path:      DefaultLocalDBPath,
		RemoteUrl: env.TursoDatabaseURL,
		AuthToken: env.TursoAuthToken,
	})
	if err != nil {
		return nil, err
	}

	db, err := syncDB.Connect(ctx)
	if err != nil {
		return nil, err
	}

	s := &Store{db: db, syncDB: syncDB}
	s.syncDB.Pull(ctx)

	if err := s.Migrate(ctx); err != nil {
		return nil, err
	}
	if err := s.SeedDefaults(ctx); err != nil {
		return nil, err
	}
	s.syncDB.Push(ctx)

	return s, nil
}

func (s *Store) Close() {
	if s.db != nil {
		_ = s.db.Close()
	}
}

func (s *Store) Migrate(ctx context.Context) error {
	queries := []string{
		`CREATE TABLE IF NOT EXISTS settings (
			key TEXT PRIMARY KEY,
			value TEXT NOT NULL,
			updated_at TEXT NOT NULL
		);`,
		`CREATE TABLE IF NOT EXISTS events (
			id TEXT PRIMARY KEY,
			level TEXT NOT NULL,
			event_type TEXT NOT NULL,
			message TEXT NOT NULL,
			data_json TEXT,
			created_at TEXT NOT NULL
		);`,
		`CREATE TABLE IF NOT EXISTS positions (
			id TEXT PRIMARY KEY,
			mode TEXT NOT NULL,
			mint TEXT NOT NULL,
			symbol TEXT NOT NULL,
			entry_price_usd REAL NOT NULL,
			last_price_usd REAL NOT NULL,
			highest_price_usd REAL NOT NULL,
			amount_token_raw TEXT,
			amount_token_est REAL NOT NULL,
			amount_usd REAL NOT NULL,
			amount_idr REAL NOT NULL,
			status TEXT NOT NULL,
			tp1_done INTEGER NOT NULL,
			tp2_done INTEGER NOT NULL,
			entry_tx TEXT,
			exit_tx TEXT,
			opened_at TEXT NOT NULL,
			closed_at TEXT,
			last_reason TEXT
		);`,
		`CREATE TABLE IF NOT EXISTS trades (
			id TEXT PRIMARY KEY,
			position_id TEXT NOT NULL,
			mode TEXT NOT NULL,
			side TEXT NOT NULL,
			mint TEXT NOT NULL,
			symbol TEXT NOT NULL,
			price_usd REAL NOT NULL,
			amount_token_raw TEXT,
			amount_token_est REAL NOT NULL,
			amount_usd REAL NOT NULL,
			tx_signature TEXT,
			reason TEXT,
			created_at TEXT NOT NULL
		);`,
		`CREATE TABLE IF NOT EXISTS telegram_state (
			key TEXT PRIMARY KEY,
			value TEXT NOT NULL,
			updated_at TEXT NOT NULL
		);`,
	}

	for _, q := range queries {
		if _, err := s.db.ExecContext(ctx, q); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) SeedDefaults(ctx context.Context) error {
	defaults := map[string]string{
		"trading.mode":                  "paper",
		"trading.allow_autobuy_paper":   "true",
		"trading.allow_autobuy_real":    "false",
		"trading.approval_required_real": "true",

		"risk.max_open_positions":       "1",
		"risk.max_position_idr":         "10000",
		"risk.max_daily_loss_idr":       "15000",
		"risk.max_daily_trades":         "3",

		"exit.stop_loss_pct":            "-20",
		"exit.take_profit_1_pct":        "35",
		"exit.take_profit_1_sell_pct":   "50",
		"exit.take_profit_2_pct":        "70",
		"exit.trailing_start_pct":       "35",
		"exit.trailing_distance_pct":    "18",
		"exit.max_hold_minutes":         "45",

		"swap.slippage_bps":             "700",

		"interval.scanner_seconds":      "35",
		"interval.position_seconds":     "10",
		"interval.telegram_edit_seconds": "6",

		"market.usd_idr":                "16500",
		"market.sol_usd":                "150",

		"scanner.max_profiles":          "25",
		"scanner.min_liquidity_usd":     "8000",
		"scanner.max_liquidity_usd":     "300000",
		"scanner.min_volume_5m_usd":     "3000",
		"scanner.min_txns_5m":           "30",
		"scanner.min_buys_5m":           "18",
		"scanner.max_sell_ratio_5m":     "0.55",
		"scanner.min_pair_age_minutes":  "10",
		"scanner.max_pair_age_hours":    "12",
		"scanner.max_price_change_5m_pct": "90",
		"scanner.max_price_change_1h_pct": "260",
		"scanner.min_score":             "70",

		"ai.enabled":                    "true",
		"ai.min_confidence":             "0.70",
		"ai.model_priority":             "gemini-3.5-flash,gemini-3.1-flash-lite,gemini-3-flash-preview,gemini-2.5-flash,gemini-2.5-flash-lite",
		"ai.model_cooldown_minutes":     "30",

		"api.dexscreener_base_url":      DefaultDexScreenerBase,
		"api.gemini_base_url":           DefaultGeminiBaseURL,
		"api.jupiter_base_url":          DefaultJupiterBaseURL,
	}

	for k, v := range defaults {
		var exists int
		err := s.db.QueryRowContext(ctx, "SELECT COUNT(1) FROM settings WHERE key = ?", k).Scan(&exists)
		if err != nil {
			return err
		}
		if exists == 0 {
			if _, err := s.db.ExecContext(ctx,
				"INSERT INTO settings(key, value, updated_at) VALUES(?, ?, ?)",
				k, v, now(),
			); err != nil {
				return err
			}
		}
	}

	return nil
}

func (s *Store) GetAllSettings(ctx context.Context) (map[string]string, error) {
	rows, err := s.db.QueryContext(ctx, "SELECT key, value FROM settings")
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := map[string]string{}
	for rows.Next() {
		var k, v string
		if err := rows.Scan(&k, &v); err != nil {
			return nil, err
		}
		out[k] = v
	}
	return out, rows.Err()
}

func (s *Store) SetSetting(ctx context.Context, key, value string) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO settings(key, value, updated_at)
		 VALUES(?, ?, ?)
		 ON CONFLICT(key) DO UPDATE SET value = excluded.value, updated_at = excluded.updated_at`,
		key, value, now(),
	)
	if err == nil {
		s.syncDB.Push(ctx)
	}
	return err
}

func (s *Store) GetTelegramState(ctx context.Context, key string) (string, error) {
	var v string
	err := s.db.QueryRowContext(ctx, "SELECT value FROM telegram_state WHERE key = ?", key).Scan(&v)
	return v, err
}

func (s *Store) SetTelegramState(ctx context.Context, key, value string) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO telegram_state(key, value, updated_at)
		 VALUES(?, ?, ?)
		 ON CONFLICT(key) DO UPDATE SET value = excluded.value, updated_at = excluded.updated_at`,
		key, value, now(),
	)
	if err == nil {
		s.syncDB.Push(ctx)
	}
	return err
}

func (s *Store) SaveEvent(ctx context.Context, level, typ, msg string, data any) {
	raw := ""
	if data != nil {
		b, _ := json.Marshal(data)
		raw = string(b)
	}
	_, _ = s.db.ExecContext(ctx,
		"INSERT INTO events(id, level, event_type, message, data_json, created_at) VALUES(?, ?, ?, ?, ?, ?)",
		makeID("evt"), level, typ, msg, raw, now(),
	)
	s.syncDB.Push(ctx)
}

func (s *Store) SavePosition(ctx context.Context, p *Position) error {
	closed := ""
	if p.ClosedAt != nil {
		closed = p.ClosedAt.Format(time.RFC3339)
	}
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO positions(
			id, mode, mint, symbol, entry_price_usd, last_price_usd, highest_price_usd,
			amount_token_raw, amount_token_est, amount_usd, amount_idr, status,
			tp1_done, tp2_done, entry_tx, exit_tx, opened_at, closed_at, last_reason
		) VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
			last_price_usd = excluded.last_price_usd,
			highest_price_usd = excluded.highest_price_usd,
			amount_token_raw = excluded.amount_token_raw,
			amount_token_est = excluded.amount_token_est,
			status = excluded.status,
			tp1_done = excluded.tp1_done,
			tp2_done = excluded.tp2_done,
			exit_tx = excluded.exit_tx,
			closed_at = excluded.closed_at,
			last_reason = excluded.last_reason`,
		p.ID, p.Mode, p.Mint, p.Symbol, p.EntryPriceUSD, p.LastPriceUSD, p.HighestPriceUSD,
		p.AmountTokenRaw, p.AmountTokenEst, p.AmountUSD, p.AmountIDR, p.Status,
		boolInt(p.TP1Done), boolInt(p.TP2Done), p.EntryTx, p.ExitTx, p.OpenedAt.Format(time.RFC3339), closed, p.LastReason,
	)
	if err == nil {
		s.syncDB.Push(ctx)
	}
	return err
}

func (s *Store) SaveTrade(ctx context.Context, p *Position, side, tx, reason string, amountRaw string, amountEst float64, amountUSD float64) {
	_, _ = s.db.ExecContext(ctx,
		`INSERT INTO trades(id, position_id, mode, side, mint, symbol, price_usd, amount_token_raw, amount_token_est, amount_usd, tx_signature, reason, created_at)
		 VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		makeID("trd"), p.ID, p.Mode, side, p.Mint, p.Symbol, p.LastPriceUSD, amountRaw, amountEst, amountUSD, tx, reason, now(),
	)
	s.syncDB.Push(ctx)
}

func (s *Store) LoadOpenPosition(ctx context.Context) (*Position, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT id, mode, mint, symbol, entry_price_usd, last_price_usd, highest_price_usd,
		        amount_token_raw, amount_token_est, amount_usd, amount_idr, status, tp1_done, tp2_done,
		        entry_tx, exit_tx, opened_at, closed_at, last_reason
		   FROM positions WHERE status = 'open' ORDER BY opened_at DESC LIMIT 1`,
	)

	var p Position
	var opened, closed string
	var tp1, tp2 int
	err := row.Scan(
		&p.ID, &p.Mode, &p.Mint, &p.Symbol, &p.EntryPriceUSD, &p.LastPriceUSD, &p.HighestPriceUSD,
		&p.AmountTokenRaw, &p.AmountTokenEst, &p.AmountUSD, &p.AmountIDR, &p.Status, &tp1, &tp2,
		&p.EntryTx, &p.ExitTx, &opened, &closed, &p.LastReason,
	)
	if err != nil {
		return nil, err
	}
	p.TP1Done = tp1 == 1
	p.TP2Done = tp2 == 1
	p.OpenedAt, _ = time.Parse(time.RFC3339, opened)
	if closed != "" {
		t, _ := time.Parse(time.RFC3339, closed)
		p.ClosedAt = &t
	}
	return &p, nil
}

func (a *App) reloadConfig(ctx context.Context) error {
	settings, err := a.store.GetAllSettings(ctx)
	if err != nil {
		return err
	}

	cfg := Config{
		TradingMode:           strSetting(settings, "trading.mode", "paper"),
		AllowAutobuyPaper:     boolSetting(settings, "trading.allow_autobuy_paper", true),
		AllowAutobuyReal:      boolSetting(settings, "trading.allow_autobuy_real", false),
		ApprovalRequiredReal:  boolSetting(settings, "trading.approval_required_real", true),
		MaxOpenPositions:      intSetting(settings, "risk.max_open_positions", 1),
		MaxPositionIDR:        floatSetting(settings, "risk.max_position_idr", 10000),
		MaxDailyLossIDR:       floatSetting(settings, "risk.max_daily_loss_idr", 15000),
		MaxDailyTrades:        intSetting(settings, "risk.max_daily_trades", 3),
		StopLossPct:           floatSetting(settings, "exit.stop_loss_pct", -20),
		TakeProfit1Pct:        floatSetting(settings, "exit.take_profit_1_pct", 35),
		TakeProfit1SellPct:    floatSetting(settings, "exit.take_profit_1_sell_pct", 50),
		TakeProfit2Pct:        floatSetting(settings, "exit.take_profit_2_pct", 70),
		TrailingStartPct:      floatSetting(settings, "exit.trailing_start_pct", 35),
		TrailingDistancePct:   floatSetting(settings, "exit.trailing_distance_pct", 18),
		MaxHoldMinutes:        intSetting(settings, "exit.max_hold_minutes", 45),
		SlippageBps:           intSetting(settings, "swap.slippage_bps", 700),
		ScannerIntervalSec:    intSetting(settings, "interval.scanner_seconds", 35),
		PositionIntervalSec:   intSetting(settings, "interval.position_seconds", 10),
		TelegramEditInterval:  intSetting(settings, "interval.telegram_edit_seconds", 6),
		USDIDR:                floatSetting(settings, "market.usd_idr", 16500),
		SOLUSD:                floatSetting(settings, "market.sol_usd", 150),
		ScannerMaxProfiles:    intSetting(settings, "scanner.max_profiles", 25),
		MinLiquidityUSD:       floatSetting(settings, "scanner.min_liquidity_usd", 8000),
		MaxLiquidityUSD:       floatSetting(settings, "scanner.max_liquidity_usd", 300000),
		MinVolume5mUSD:        floatSetting(settings, "scanner.min_volume_5m_usd", 3000),
		MinTxns5m:             intSetting(settings, "scanner.min_txns_5m", 30),
		MinBuys5m:             intSetting(settings, "scanner.min_buys_5m", 18),
		MaxSellRatio5m:        floatSetting(settings, "scanner.max_sell_ratio_5m", 0.55),
		MinPairAgeMinutes:     intSetting(settings, "scanner.min_pair_age_minutes", 10),
		MaxPairAgeHours:       intSetting(settings, "scanner.max_pair_age_hours", 12),
		MaxPriceChange5mPct:   floatSetting(settings, "scanner.max_price_change_5m_pct", 90),
		MaxPriceChange1hPct:   floatSetting(settings, "scanner.max_price_change_1h_pct", 260),
		MinScore:              floatSetting(settings, "scanner.min_score", 70),
		AIEnabled:             boolSetting(settings, "ai.enabled", true),
		AIMinConfidence:       floatSetting(settings, "ai.min_confidence", 0.70),
		AIModelPriority:       csvSetting(settings, "ai.model_priority"),
		AIModelCooldown:       intSetting(settings, "ai.model_cooldown_minutes", 30),
		DexScreenerBaseURL:    strSetting(settings, "api.dexscreener_base_url", DefaultDexScreenerBase),
		GeminiBaseURL:         strSetting(settings, "api.gemini_base_url", DefaultGeminiBaseURL),
		JupiterBaseURL:        strSetting(settings, "api.jupiter_base_url", DefaultJupiterBaseURL),
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
		Mint:           chosen.BaseToken.Address,
		Symbol:         chosen.BaseToken.Symbol,
		Name:           chosen.BaseToken.Name,
		PairAddress:    chosen.PairAddress,
		DexID:          chosen.DexID,
		URL:            chosen.URL,
		PriceUSD:       price,
		LiquidityUSD:   chosen.Liquidity.USD,
		Volume5mUSD:    chosen.Volume.M5,
		Buys5m:         chosen.Txns.M5.Buys,
		Sells5m:        chosen.Txns.M5.Sells,
		Txns5m:         txns,
		SellRatio5m:    sellRatio,
		PriceChange5m:  chosen.PriceChange.M5,
		PriceChange1h:  chosen.PriceChange.H1,
		PairAgeMin:     ageMin,
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

func (a *App) analyzeWithGemini(ctx context.Context, cfg Config, c *Candidate) (*AIResult, error) {
	payload := map[string]any{
		"token": c.Symbol,
		"mint": c.Mint,
		"pair_age_minutes": c.PairAgeMin,
		"liquidity_usd": c.LiquidityUSD,
		"volume_5m_usd": c.Volume5mUSD,
		"buys_5m": c.Buys5m,
		"sells_5m": c.Sells5m,
		"sell_ratio_5m": c.SellRatio5m,
		"price_change_5m_pct": c.PriceChange5m,
		"price_change_1h_pct": c.PriceChange1h,
		"score": c.Score,
	}

	payloadJSON, _ := json.MarshalIndent(payload, "", "  ")

	system := `You are an AI risk analyst for a Solana memecoin micro-trading bot.
You never execute trades. You only return JSON.
Be conservative. If risk is unclear, reject.
Return only:
{"verdict":"PASS|REJECT","confidence":0.0,"risk":"LOW|MEDIUM|HIGH","reason":"short reason","max_hold_minutes":30}`

	user := "Analyze this Solana memecoin candidate for a very small high-risk spot trade.\n\n" + string(payloadJSON)

	for _, model := range cfg.AIModelPriority {
		res, err := a.callGemini(ctx, cfg, model, system, user)
		if err != nil {
			continue
		}
		res.ModelUsed = model
		if res.Verdict == "" {
			continue
		}
		return res, nil
	}

	return &AIResult{
		Verdict:     "REJECT",
		Confidence:  0,
		Risk:        "HIGH",
		Reason:      "All Gemini models failed or returned invalid JSON.",
		ModelUsed:   "none",
	}, nil
}

func (a *App) callGemini(ctx context.Context, cfg Config, model, system, user string) (*AIResult, error) {
	reqBody := GeminiRequest{
		SystemInstruction: &GeminiSystemInstruction{
			Parts: []GeminiPart{{Text: system}},
		},
		Contents: []GeminiContent{
			{Parts: []GeminiPart{{Text: user}}},
		},
		GenerationConfig: GeminiGenerationConfig{
			ResponseMIMEType: "application/json",
			Temperature:      0.2,
		},
	}

	raw, _ := json.Marshal(reqBody)
	endpoint := fmt.Sprintf("%s/models/%s:generateContent?key=%s",
		strings.TrimRight(cfg.GeminiBaseURL, "/"),
		url.PathEscape(model),
		url.QueryEscape(a.env.GeminiAPIKey),
	)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(raw))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := a.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 300 {
		return nil, fmt.Errorf("gemini %s HTTP %d: %s", model, resp.StatusCode, string(body))
	}

	var gr GeminiResponse
	if err := json.Unmarshal(body, &gr); err != nil {
		return nil, err
	}
	if gr.Error != nil {
		return nil, errors.New(gr.Error.Message)
	}
	if len(gr.Candidates) == 0 || len(gr.Candidates[0].Content.Parts) == 0 {
		return nil, errors.New("empty gemini response")
	}

	text := strings.TrimSpace(gr.Candidates[0].Content.Parts[0].Text)
	text = strings.TrimPrefix(text, "```json")
	text = strings.TrimPrefix(text, "```")
	text = strings.TrimSuffix(text, "```")
	text = strings.TrimSpace(text)

	var out AIResult
	if err := json.Unmarshal([]byte(text), &out); err != nil {
		return nil, err
	}
	out.Verdict = strings.ToUpper(strings.TrimSpace(out.Verdict))
	out.Risk = strings.ToUpper(strings.TrimSpace(out.Risk))
	if out.MaxHoldMinutes <= 0 {
		out.MaxHoldMinutes = 30
	}
	return &out, nil
}

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

func (a *App) executeJupiterSwap(ctx context.Context, cfg Config, inputMint, outputMint, amount string) (string, string, error) {
	pk, err := solana.PrivateKeyFromBase58(a.env.PrivateKeyBase58)
	if err != nil {
		return "", "", err
	}
	taker := pk.PublicKey().String()

	orderURL := strings.TrimRight(cfg.JupiterBaseURL, "/") + "/order"
	q := url.Values{}
	q.Set("inputMint", inputMint)
	q.Set("outputMint", outputMint)
	q.Set("amount", amount)
	q.Set("taker", taker)
	q.Set("slippageBps", strconv.Itoa(cfg.SlippageBps))

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, orderURL+"?"+q.Encode(), nil)
	if err != nil {
		return "", "", err
	}
	req.Header.Set("x-api-key", a.env.JupiterAPIKey)

	resp, err := a.client.Do(req)
	if err != nil {
		return "", "", err
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 300 {
		return "", "", fmt.Errorf("jupiter order HTTP %d: %s", resp.StatusCode, string(body))
	}

	var order JupiterOrderResponse
	if err := json.Unmarshal(body, &order); err != nil {
		return "", "", err
	}
	if order.Error != "" {
		return "", "", errors.New(order.Error)
	}
	if order.Transaction == "" || order.RequestID == "" {
		return "", "", fmt.Errorf("invalid jupiter order: %s", string(body))
	}

	signedB64, err := signJupiterTransaction(order.Transaction, pk)
	if err != nil {
		return "", "", err
	}

	executeURL := strings.TrimRight(cfg.JupiterBaseURL, "/") + "/execute"
	execPayload := map[string]string{
		"signedTransaction": signedB64,
		"requestId":         order.RequestID,
	}
	rawPayload, _ := json.Marshal(execPayload)

	req2, err := http.NewRequestWithContext(ctx, http.MethodPost, executeURL, bytes.NewReader(rawPayload))
	if err != nil {
		return "", "", err
	}
	req2.Header.Set("Content-Type", "application/json")
	req2.Header.Set("x-api-key", a.env.JupiterAPIKey)

	resp2, err := a.client.Do(req2)
	if err != nil {
		return "", "", err
	}
	defer resp2.Body.Close()

	body2, _ := io.ReadAll(resp2.Body)
	if resp2.StatusCode >= 300 {
		return "", "", fmt.Errorf("jupiter execute HTTP %d: %s", resp2.StatusCode, string(body2))
	}

	var ex JupiterExecuteResponse
	if err := json.Unmarshal(body2, &ex); err != nil {
		return "", "", err
	}
	if ex.Error != "" {
		return "", "", errors.New(ex.Error)
	}
	if ex.Signature == "" {
		return "", "", fmt.Errorf("empty signature: %s", string(body2))
	}

	return ex.Signature, order.OutAmount, nil
}

func signJupiterTransaction(txBase64 string, pk solana.PrivateKey) (string, error) {
	raw, err := base64.StdEncoding.DecodeString(txBase64)
	if err != nil {
		return "", err
	}

	tx, err := solana.TransactionFromDecoder(bin.NewBinDecoder(raw))
	if err != nil {
		return "", err
	}

	_, err = tx.Sign(func(key solana.PublicKey) *solana.PrivateKey {
		if key.Equals(pk.PublicKey()) {
			return &pk
		}
		return nil
	})
	if err != nil {
		return "", err
	}

	signed, err := tx.MarshalBinary()
	if err != nil {
		return "", err
	}

	return base64.StdEncoding.EncodeToString(signed), nil
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

func (a *App) telegramPoller(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		default:
			a.pollTelegramOnce(ctx)
			time.Sleep(1 * time.Second)
		}
	}
}

func (a *App) pollTelegramOnce(ctx context.Context) {
	endpoint := fmt.Sprintf("https://api.telegram.org/bot%s/getUpdates", a.env.TelegramBotToken)
	q := url.Values{}
	q.Set("timeout", "20")
	q.Set("offset", strconv.FormatInt(a.updateOffset, 10))
	q.Set("allowed_updates", `["message","callback_query"]`)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint+"?"+q.Encode(), nil)
	if err != nil {
		return
	}

	resp, err := a.client.Do(req)
	if err != nil {
		return
	}
	defer resp.Body.Close()

	var out TelegramUpdateResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return
	}
	if !out.OK {
		return
	}

	for _, upd := range out.Result {
		if upd.UpdateID >= a.updateOffset {
			a.updateOffset = upd.UpdateID + 1
		}
		a.handleTelegramUpdate(ctx, upd)
	}
}

func (a *App) handleTelegramUpdate(ctx context.Context, upd TelegramUpdate) {
	if upd.Message != nil {
		if upd.Message.From.ID != a.env.TelegramOwnerID {
			_ = a.sendMessage(ctx, upd.Message.Chat.ID, "Unauthorized.", nil)
			return
		}
		a.handleCommand(ctx, upd.Message.Chat.ID, strings.TrimSpace(upd.Message.Text))
		return
	}

	if upd.CallbackQuery != nil {
		if upd.CallbackQuery.From.ID != a.env.TelegramOwnerID {
			_ = a.answerCallback(ctx, upd.CallbackQuery.ID, "Unauthorized")
			return
		}
		a.handleCallback(ctx, upd.CallbackQuery.ID, upd.CallbackQuery.Message.Chat.ID, upd.CallbackQuery.Data)
	}
}

func (a *App) handleCommand(ctx context.Context, chatID int64, text string) {
	if text == "" {
		return
	}
	parts := strings.Fields(text)
	cmd := strings.ToLower(parts[0])

	switch cmd {
	case "/start", "/dashboard":
		_ = a.store.SetTelegramState(ctx, "chat_id", strconv.FormatInt(chatID, 10))
		_ = a.sendOrEditDashboard(ctx, true)
	case "/help":
		_ = a.sendMessage(ctx, chatID, helpText(), nil)
	case "/config":
		_ = a.reloadConfig(ctx)
		_ = a.sendMessage(ctx, chatID, a.configText(), nil)
	case "/set":
		if len(parts) < 3 {
			_ = a.sendMessage(ctx, chatID, "Format: /set key value\nContoh: /set risk.max_position_idr 10000", nil)
			return
		}
		key := parts[1]
		value := strings.Join(parts[2:], " ")
		if err := a.store.SetSetting(ctx, key, value); err != nil {
			_ = a.sendMessage(ctx, chatID, "Error: "+err.Error(), nil)
			return
		}
		_ = a.reloadConfig(ctx)
		_ = a.sendMessage(ctx, chatID, "✅ Setting updated: "+key+" = "+value, nil)
	case "/mode":
		if len(parts) < 2 {
			_ = a.sendMessage(ctx, chatID, "Format: /mode paper atau /mode real", nil)
			return
		}
		mode := strings.ToLower(parts[1])
		if mode != "paper" && mode != "real" {
			_ = a.sendMessage(ctx, chatID, "Mode harus paper/real.", nil)
			return
		}
		_ = a.store.SetSetting(ctx, "trading.mode", mode)
		_ = a.reloadConfig(ctx)
		_ = a.sendMessage(ctx, chatID, "✅ Mode: "+mode, nil)
	case "/autobuy_paper":
		a.setOnOff(ctx, chatID, parts, "trading.allow_autobuy_paper")
	case "/autobuy_real":
		a.setOnOff(ctx, chatID, parts, "trading.allow_autobuy_real")
	case "/pause":
		a.mu.Lock()
		a.paused = true
		a.lastAction = "Paused by command."
		a.mu.Unlock()
		_ = a.sendMessage(ctx, chatID, "⏸ Paused.", nil)
	case "/resume":
		a.mu.Lock()
		a.paused = false
		a.lastAction = "Resumed by command."
		a.mu.Unlock()
		_ = a.sendMessage(ctx, chatID, "▶️ Resumed.", nil)
	case "/scan":
		_ = a.sendMessage(ctx, chatID, "Scanning sekali...", nil)
		go a.scanOnce(context.Background())
	case "/buy_paper":
		if err := a.buyCandidate(ctx, "paper"); err != nil {
			_ = a.sendMessage(ctx, chatID, "Error: "+err.Error(), nil)
			return
		}
		_ = a.sendMessage(ctx, chatID, "✅ Paper buy executed.", nil)
	case "/buy_real":
		if err := a.buyCandidate(ctx, "real"); err != nil {
			_ = a.sendMessage(ctx, chatID, "Error: "+err.Error(), nil)
			return
		}
		_ = a.sendMessage(ctx, chatID, "✅ Real buy executed.", nil)
	case "/sell_now", "/stop_holding":
		if err := a.sellCurrent(ctx, "manual stop"); err != nil {
			_ = a.sendMessage(ctx, chatID, "Error: "+err.Error(), nil)
			return
		}
		_ = a.sendMessage(ctx, chatID, "✅ Sell/stop executed.", nil)
	default:
		_ = a.sendMessage(ctx, chatID, "Command tidak dikenal. Ketik /help", nil)
	}
}

func (a *App) setOnOff(ctx context.Context, chatID int64, parts []string, key string) {
	if len(parts) < 2 {
		_ = a.sendMessage(ctx, chatID, "Format: "+parts[0]+" on/off", nil)
		return
	}
	v := strings.ToLower(parts[1])
	if v != "on" && v != "off" {
		_ = a.sendMessage(ctx, chatID, "Pakai on/off.", nil)
		return
	}
	val := "false"
	if v == "on" {
		val = "true"
	}
	_ = a.store.SetSetting(ctx, key, val)
	_ = a.reloadConfig(ctx)
	_ = a.sendMessage(ctx, chatID, "✅ "+key+" = "+val, nil)
}

func (a *App) handleCallback(ctx context.Context, cbID string, chatID int64, data string) {
	var msg string

	switch data {
	case "refresh":
		msg = "Refreshed."
	case "pause":
		a.mu.Lock()
		a.paused = true
		a.lastAction = "Paused by button."
		a.mu.Unlock()
		msg = "Paused."
	case "resume":
		a.mu.Lock()
		a.paused = false
		a.lastAction = "Resumed by button."
		a.mu.Unlock()
		msg = "Resumed."
	case "mode_paper":
		_ = a.store.SetSetting(ctx, "trading.mode", "paper")
		_ = a.reloadConfig(ctx)
		msg = "Mode paper."
	case "mode_real":
		_ = a.store.SetSetting(ctx, "trading.mode", "real")
		_ = a.reloadConfig(ctx)
		msg = "Mode real."
	case "buy_paper":
		if err := a.buyCandidate(ctx, "paper"); err != nil {
			msg = "Error: " + err.Error()
		} else {
			msg = "Paper buy executed."
		}
	case "buy_real":
		if err := a.buyCandidate(ctx, "real"); err != nil {
			msg = "Error: " + err.Error()
		} else {
			msg = "Real buy executed."
		}
	case "sell_now":
		if err := a.sellCurrent(ctx, "manual button"); err != nil {
			msg = "Error: " + err.Error()
		} else {
			msg = "Sell executed."
		}
	case "skip":
		a.mu.Lock()
		a.candidate = nil
		a.lastAction = "Candidate skipped."
		a.mu.Unlock()
		msg = "Skipped."
	case "toggle_autobuy_real":
		a.mu.Lock()
		current := a.cfg.AllowAutobuyReal
		a.mu.Unlock()
		_ = a.store.SetSetting(ctx, "trading.allow_autobuy_real", boolStr(!current))
		_ = a.reloadConfig(ctx)
		msg = "AutoBuy real toggled."
	default:
		msg = "Unknown action."
	}

	_ = a.answerCallback(ctx, cbID, msg)
	_ = a.sendOrEditDashboard(ctx, true)
}

func (a *App) dashboardLoop(ctx context.Context) {
	for {
		a.mu.Lock()
		interval := time.Duration(a.cfg.TelegramEditInterval) * time.Second
		a.mu.Unlock()

		select {
		case <-ctx.Done():
			return
		case <-time.After(interval):
			_ = a.sendOrEditDashboard(ctx, false)
		}
	}
}

func (a *App) sendOrEditDashboard(ctx context.Context, force bool) error {
	chatStr, err := a.store.GetTelegramState(ctx, "chat_id")
	if err != nil || chatStr == "" {
		return nil
	}
	chatID, _ := strconv.ParseInt(chatStr, 10, 64)
	if chatID == 0 {
		return nil
	}

	text := a.dashboardText()
	hash := sha(text)
	lastHash, _ := a.store.GetTelegramState(ctx, "dashboard_hash")
	msgIDStr, _ := a.store.GetTelegramState(ctx, "dashboard_message_id")

	if !force && lastHash == hash {
		return nil
	}

	keyboard := dashboardKeyboard()

	if msgIDStr == "" {
		msgID, err := a.sendMessage(ctx, chatID, text, keyboard)
		if err != nil {
			return err
		}
		_ = a.store.SetTelegramState(ctx, "dashboard_message_id", strconv.FormatInt(msgID, 10))
		_ = a.store.SetTelegramState(ctx, "dashboard_hash", hash)
		return nil
	}

	msgID, _ := strconv.ParseInt(msgIDStr, 10, 64)
	if msgID == 0 {
		return nil
	}
	if err := a.editMessage(ctx, chatID, msgID, text, keyboard); err != nil {
		msgID, err2 := a.sendMessage(ctx, chatID, text, keyboard)
		if err2 != nil {
			return err
		}
		_ = a.store.SetTelegramState(ctx, "dashboard_message_id", strconv.FormatInt(msgID, 10))
	}
	_ = a.store.SetTelegramState(ctx, "dashboard_hash", hash)
	return nil
}

func dashboardKeyboard() *InlineKeyboardMarkup {
	return &InlineKeyboardMarkup{
		InlineKeyboard: [][]InlineKeyboardButton{
			{
				{Text: "🔄 Refresh", CallbackData: "refresh"},
				{Text: "⏸ Pause", CallbackData: "pause"},
				{Text: "▶️ Resume", CallbackData: "resume"},
			},
			{
				{Text: "🧪 Mode Paper", CallbackData: "mode_paper"},
				{Text: "🔥 Mode Real", CallbackData: "mode_real"},
			},
			{
				{Text: "✅ Buy Paper", CallbackData: "buy_paper"},
				{Text: "⚠️ Buy Real", CallbackData: "buy_real"},
			},
			{
				{Text: "🛑 SELL NOW", CallbackData: "sell_now"},
				{Text: "⏭ Skip", CallbackData: "skip"},
			},
			{
				{Text: "🤖 Toggle AutoBuy Real", CallbackData: "toggle_autobuy_real"},
			},
		},
	}
}

func (a *App) dashboardText() string {
	a.mu.Lock()
	defer a.mu.Unlock()

	cfg := a.cfg
	status := "SCANNING"
	if a.paused {
		status = "PAUSED"
	}
	if a.position != nil && a.position.Status == "open" {
		status = "HOLDING"
	} else if a.candidate != nil {
		status = "CANDIDATE_READY"
	}

	var b strings.Builder
	b.WriteString("🤖 *auto-tr-ai — Solana Micro Agent*\n\n")
	b.WriteString(fmt.Sprintf("*Mode:* `%s`\n", strings.ToUpper(cfg.TradingMode)))
	b.WriteString(fmt.Sprintf("*Status:* `%s`\n", status))
	b.WriteString(fmt.Sprintf("*AutoBuy Paper:* `%v`\n", cfg.AllowAutobuyPaper))
	b.WriteString(fmt.Sprintf("*AutoBuy Real:* `%v`\n", cfg.AllowAutobuyReal))
	b.WriteString(fmt.Sprintf("*Real Approval Required:* `%v`\n\n", cfg.ApprovalRequiredReal))

	b.WriteString("🛡 *Risk*\n")
	b.WriteString(fmt.Sprintf("Max position: `Rp%.0f`\n", cfg.MaxPositionIDR))
	b.WriteString(fmt.Sprintf("Max daily loss: `Rp%.0f`\n", cfg.MaxDailyLossIDR))
	b.WriteString(fmt.Sprintf("SL: `%.1f%%` | TP1: `%.1f%%` | TP2: `%.1f%%`\n", cfg.StopLossPct, cfg.TakeProfit1Pct, cfg.TakeProfit2Pct))
	b.WriteString(fmt.Sprintf("Trailing: start `%.1f%%`, distance `%.1f%%`\n\n", cfg.TrailingStartPct, cfg.TrailingDistancePct))

	if a.candidate != nil {
		c := a.candidate
		b.WriteString("🎯 *Candidate*\n")
		b.WriteString(fmt.Sprintf("Token: `%s`\n", escape(c.Symbol)))
		b.WriteString(fmt.Sprintf("Mint: `%s`\n", c.Mint))
		b.WriteString(fmt.Sprintf("Price: `$%.10f`\n", c.PriceUSD))
		b.WriteString(fmt.Sprintf("Liq: `$%.0f` | Vol5m: `$%.0f`\n", c.LiquidityUSD, c.Volume5mUSD))
		b.WriteString(fmt.Sprintf("Buys/Sells 5m: `%d/%d`\n", c.Buys5m, c.Sells5m))
		b.WriteString(fmt.Sprintf("Age: `%.1f min` | Score: `%.1f`\n", c.PairAgeMin, c.Score))
		if c.AI != nil {
			b.WriteString(fmt.Sprintf("AI: `%s` conf `%.2f` risk `%s`\n", c.AI.Verdict, c.AI.Confidence, c.AI.Risk))
			b.WriteString(fmt.Sprintf("AI model: `%s`\n", c.AI.ModelUsed))
			b.WriteString(fmt.Sprintf("Reason: `%s`\n", escape(c.AI.Reason)))
		}
		b.WriteString("\n")
	}

	if a.position != nil && a.position.Status == "open" {
		p := a.position
		pnl := ((p.LastPriceUSD - p.EntryPriceUSD) / p.EntryPriceUSD) * 100
		b.WriteString("📌 *Position*\n")
		b.WriteString(fmt.Sprintf("Token: `%s` | Mode: `%s`\n", escape(p.Symbol), p.Mode))
		b.WriteString(fmt.Sprintf("Entry: `$%.10f`\n", p.EntryPriceUSD))
		b.WriteString(fmt.Sprintf("Last: `$%.10f`\n", p.LastPriceUSD))
		b.WriteString(fmt.Sprintf("PnL: `%.2f%%`\n", pnl))
		b.WriteString(fmt.Sprintf("Hold: `%.1f min`\n", time.Since(p.OpenedAt).Minutes()))
		b.WriteString(fmt.Sprintf("TP1 done: `%v`\n", p.TP1Done))
		b.WriteString("\n")
	}

	b.WriteString("🧠 *AI Models*\n")
	b.WriteString("`" + strings.Join(cfg.AIModelPriority, ", ") + "`\n\n")

	b.WriteString("🕒 *Runtime*\n")
	b.WriteString(fmt.Sprintf("Last scan: `%s`\n", a.lastScanAt.Format("15:04:05")))
	b.WriteString(fmt.Sprintf("Last action: `%s`\n", escape(a.lastAction)))
	if a.lastError != "" {
		b.WriteString(fmt.Sprintf("Last error: `%s`\n", escape(a.lastError)))
	}

	return b.String()
}

func (a *App) configText() string {
	a.mu.Lock()
	cfg := a.cfg
	a.mu.Unlock()

	return fmt.Sprintf(`⚙️ *Config Aktif*

*Trading*
mode = %s
allow_autobuy_paper = %v
allow_autobuy_real = %v
approval_required_real = %v

*Risk*
max_position_idr = %.0f
max_daily_loss_idr = %.0f
max_daily_trades = %d

*Exit*
stop_loss_pct = %.1f
take_profit_1_pct = %.1f
take_profit_1_sell_pct = %.1f
take_profit_2_pct = %.1f
trailing_start_pct = %.1f
trailing_distance_pct = %.1f
max_hold_minutes = %d

*Scanner*
interval = %ds
min_liquidity_usd = %.0f
min_volume_5m_usd = %.0f
min_txns_5m = %d
min_pair_age_minutes = %d
max_pair_age_hours = %d

*AI*
enabled = %v
min_confidence = %.2f
models = %s
`,
		cfg.TradingMode,
		cfg.AllowAutobuyPaper,
		cfg.AllowAutobuyReal,
		cfg.ApprovalRequiredReal,
		cfg.MaxPositionIDR,
		cfg.MaxDailyLossIDR,
		cfg.MaxDailyTrades,
		cfg.StopLossPct,
		cfg.TakeProfit1Pct,
		cfg.TakeProfit1SellPct,
		cfg.TakeProfit2Pct,
		cfg.TrailingStartPct,
		cfg.TrailingDistancePct,
		cfg.MaxHoldMinutes,
		cfg.ScannerIntervalSec,
		cfg.MinLiquidityUSD,
		cfg.MinVolume5mUSD,
		cfg.MinTxns5m,
		cfg.MinPairAgeMinutes,
		cfg.MaxPairAgeHours,
		cfg.AIEnabled,
		cfg.AIMinConfidence,
		strings.Join(cfg.AIModelPriority, ","),
	)
}

func helpText() string {
	return `📖 *auto-tr-ai Help*

*Dashboard*
/start - simpan chat dan tampilkan dashboard
/dashboard - tampilkan dashboard dinamis
/config - lihat konfigurasi aktif
/help - tampilkan command lengkap

*Mode*
/mode paper - ubah ke paper-wallet
/mode real - ubah ke real-wallet
/autobuy_paper on - aktifkan auto-buy paper
/autobuy_paper off - matikan auto-buy paper
/autobuy_real on - aktifkan auto-buy real
/autobuy_real off - matikan auto-buy real

*Scanner*
/scan - scan sekali sekarang
/pause - pause scanner
/resume - lanjutkan scanner

*Trading*
/buy_paper - beli kandidat terakhir dengan paper-wallet
/buy_real - beli kandidat terakhir dengan real-wallet
/sell_now - jual/stop holding token sekarang
/stop_holding - alias sell_now

*Setting umum*
/set risk.max_position_idr 10000
/set risk.max_daily_loss_idr 15000
/set risk.max_daily_trades 3

*Exit strategy*
/set exit.stop_loss_pct -20
/set exit.take_profit_1_pct 35
/set exit.take_profit_1_sell_pct 50
/set exit.take_profit_2_pct 70
/set exit.trailing_start_pct 35
/set exit.trailing_distance_pct 18
/set exit.max_hold_minutes 45

*Scanner filter*
/set scanner.min_liquidity_usd 8000
/set scanner.max_liquidity_usd 300000
/set scanner.min_volume_5m_usd 3000
/set scanner.min_txns_5m 30
/set scanner.min_buys_5m 18
/set scanner.max_sell_ratio_5m 0.55
/set scanner.min_pair_age_minutes 10
/set scanner.max_pair_age_hours 12
/set scanner.min_score 70

*AI*
/set ai.enabled true
/set ai.min_confidence 0.70
/set ai.model_priority gemini-3.5-flash,gemini-3.1-flash-lite,gemini-3-flash-preview,gemini-2.5-flash,gemini-2.5-flash-lite

*Market conversion*
/set market.usd_idr 16500
/set market.sol_usd 150

Catatan:
- Private key tidak bisa di-set dari Telegram.
- Real-wallet butuh PRIVATE_KEY_BASE58 di ENV Koyeb.
- Jupiter Swap V2 butuh JUPITER_API_KEY di ENV.
- Kalau AI gagal semua, bot default REJECT/NO BUY.
`
}

func (a *App) sendMessage(ctx context.Context, chatID int64, text string, keyboard *InlineKeyboardMarkup) (int64, error) {
	payload := map[string]any{
		"chat_id":    chatID,
		"text":       text,
		"parse_mode": "Markdown",
	}
	if keyboard != nil {
		payload["reply_markup"] = keyboard
	}
	var resp struct {
		OK     bool `json:"ok"`
		Result struct {
			MessageID int64 `json:"message_id"`
		} `json:"result"`
		Description string `json:"description"`
	}
	if err := a.telegramPost(ctx, "sendMessage", payload, &resp); err != nil {
		return 0, err
	}
	if !resp.OK {
		return 0, errors.New(resp.Description)
	}
	return resp.Result.MessageID, nil
}

func (a *App) editMessage(ctx context.Context, chatID, messageID int64, text string, keyboard *InlineKeyboardMarkup) error {
	payload := map[string]any{
		"chat_id":    chatID,
		"message_id": messageID,
		"text":       text,
		"parse_mode": "Markdown",
	}
	if keyboard != nil {
		payload["reply_markup"] = keyboard
	}
	var resp struct {
		OK          bool   `json:"ok"`
		Description string `json:"description"`
	}
	if err := a.telegramPost(ctx, "editMessageText", payload, &resp); err != nil {
		return err
	}
	if !resp.OK {
		return errors.New(resp.Description)
	}
	return nil
}

func (a *App) answerCallback(ctx context.Context, callbackID, text string) error {
	payload := map[string]any{
		"callback_query_id": callbackID,
		"text":              text,
		"show_alert":        false,
	}
	var resp struct {
		OK          bool   `json:"ok"`
		Description string `json:"description"`
	}
	if err := a.telegramPost(ctx, "answerCallbackQuery", payload, &resp); err != nil {
		return err
	}
	if !resp.OK {
		return errors.New(resp.Description)
	}
	return nil
}

func (a *App) telegramPost(ctx context.Context, method string, payload any, out any) error {
	raw, _ := json.Marshal(payload)
	endpoint := fmt.Sprintf("https://api.telegram.org/bot%s/%s", a.env.TelegramBotToken, method)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(raw))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := a.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 300 {
		return fmt.Errorf("telegram %s HTTP %d: %s", method, resp.StatusCode, string(body))
	}
	return json.Unmarshal(body, out)
}

func (a *App) getJSON(ctx context.Context, endpoint string, headers map[string]string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return err
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := a.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 300 {
		return fmt.Errorf("GET %s HTTP %d: %s", endpoint, resp.StatusCode, string(body))
	}
	return json.Unmarshal(body, out)
}

func (a *App) handleHealthz(w http.ResponseWriter, r *http.Request) {
	a.mu.Lock()
	defer a.mu.Unlock()

	status := map[string]any{
		"ok":            true,
		"app":           AppName,
		"mode":          a.cfg.TradingMode,
		"paused":        a.paused,
		"has_position":  a.position != nil && a.position.Status == "open",
		"has_candidate": a.candidate != nil,
		"last_scan_at":  a.lastScanAt.Format(time.RFC3339),
		"last_action":   a.lastAction,
		"last_error":    a.lastError,
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(status)
}

func (a *App) handleRoot(w http.ResponseWriter, r *http.Request) {
	_, _ = w.Write([]byte("auto-tr-ai is running. Use /healthz for status.\n"))
}

func (a *App) setAction(msg string) {
	a.mu.Lock()
	a.lastAction = msg
	a.lastError = ""
	a.mu.Unlock()
}

func (a *App) setError(scope string, err error) {
	if err == nil {
		return
	}
	msg := fmt.Sprintf("%s: %s", scope, err.Error())
	log.Println(msg)
	a.store.SaveEvent(context.Background(), "error", scope, msg, nil)
	a.mu.Lock()
	a.lastError = msg
	a.lastAction = "Error happened."
	a.mu.Unlock()
}

func strSetting(m map[string]string, key, fallback string) string {
	if v, ok := m[key]; ok && strings.TrimSpace(v) != "" {
		return strings.TrimSpace(v)
	}
	return fallback
}

func boolSetting(m map[string]string, key string, fallback bool) bool {
	v, ok := m[key]
	if !ok {
		return fallback
	}
	v = strings.ToLower(strings.TrimSpace(v))
	return v == "true" || v == "1" || v == "yes" || v == "on"
}

func intSetting(m map[string]string, key string, fallback int) int {
	v, ok := m[key]
	if !ok {
		return fallback
	}
	n, err := strconv.Atoi(strings.TrimSpace(v))
	if err != nil {
		return fallback
	}
	return n
}

func floatSetting(m map[string]string, key string, fallback float64) float64 {
	v, ok := m[key]
	if !ok {
		return fallback
	}
	f, err := strconv.ParseFloat(strings.TrimSpace(v), 64)
	if err != nil {
		return fallback
	}
	return f
}

func csvSetting(m map[string]string, key string) []string {
	raw := strSetting(m, key, "")
	if raw == "" {
		return nil
	}
	parts := strings.Split(raw, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

func makeID(prefix string) string {
	h := sha256.Sum256([]byte(fmt.Sprintf("%s-%d", prefix, time.Now().UnixNano())))
	return fmt.Sprintf("%s_%x", prefix, h[:8])
}

func now() string {
	return time.Now().Format(time.RFC3339)
}

func boolInt(v bool) int {
	if v {
		return 1
	}
	return 0
}

func boolStr(v bool) string {
	if v {
		return "true"
	}
	return "false"
}

func clamp(v, min, max float64) float64 {
	if v < min {
		return min
	}
	if v > max {
		return max
	}
	return v
}

func sha(s string) string {
	h := sha256.Sum256([]byte(s))
	return fmt.Sprintf("%x", h[:8])
}

func escape(s string) string {
	s = strings.ReplaceAll(s, "`", "'")
	s = strings.ReplaceAll(s, "*", "")
	s = strings.ReplaceAll(s, "_", "-")
	return s
}
