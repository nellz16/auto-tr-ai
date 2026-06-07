package main

import (
	"net/http"
	"sync"
	"time"
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

	GeminiAPIKey  string
	HeliusRPCURL  string
	JupiterAPIKey string

	PrivateKeyBase58 string
}

type App struct {
	env    Env
	store  *Store
	client *http.Client

	mu           sync.Mutex
	cfg          Config
	paused       bool
	lastScanAt   time.Time
	lastAction   string
	lastError    string
	candidate    *Candidate
	position     *Position
	updateOffset int64
}

type Config struct {
	TradingMode string

	AllowAutobuyPaper    bool
	AllowAutobuyReal     bool
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

	ScannerMaxProfiles  int
	MinLiquidityUSD     float64
	MaxLiquidityUSD     float64
	MinVolume5mUSD      float64
	MinTxns5m           int
	MinBuys5m           int
	MaxSellRatio5m      float64
	MinPairAgeMinutes   int
	MaxPairAgeHours     int
	MaxPriceChange5mPct float64
	MaxPriceChange1hPct float64
	MinScore            float64

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
	ID              string
	Mode            string
	Mint            string
	Symbol          string
	EntryPriceUSD   float64
	LastPriceUSD    float64
	HighestPriceUSD float64
	AmountTokenRaw  string
	AmountTokenEst  float64
	AmountUSD       float64
	AmountIDR       float64
	Status          string
	TP1Done         bool
	TP2Done         bool
	EntryTx         string
	ExitTx          string
	OpenedAt        time.Time
	ClosedAt        *time.Time
	LastReason      string
}

type AIResult struct {
	Verdict        string  `json:"verdict"`
	Confidence     float64 `json:"confidence"`
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
	Txns        struct {
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
	Contents          []GeminiContent          `json:"contents"`
	GenerationConfig  GeminiGenerationConfig   `json:"generationConfig"`
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
	Transaction               string `json:"transaction"`
	RequestID                 string `json:"requestId"`
	InAmount                  string `json:"inAmount"`
	OutAmount                 string `json:"outAmount"`
	PriceImpact               string `json:"priceImpact"`
	PriceImpactPct            string `json:"priceImpactPct"`
	SignatureFeeLamports      int64  `json:"signatureFeeLamports"`
	PrioritizationFeeLamports int64  `json:"prioritizationFeeLamports"`
	TotalTime                 int64  `json:"totalTime"`
	Error                     string `json:"error"`
	Message                   string `json:"message"`
}

type JupiterExecuteResponse struct {
	Status    string `json:"status"`
	Signature string `json:"signature"`
	Slot      int64  `json:"slot"`
	Code      int    `json:"code"`
	Error     string `json:"error"`
	Message   string `json:"message"`
}
