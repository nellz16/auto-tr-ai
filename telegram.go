package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

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
			_, _ = a.sendMessage(ctx, upd.Message.Chat.ID, "Unauthorized.", nil)
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
		_, _ = a.sendMessage(ctx, chatID, helpText(), nil)
	case "/config":
		_ = a.reloadConfig(ctx)
		_, _ = a.sendMessage(ctx, chatID, a.configText(), nil)
	case "/set":
		if len(parts) < 3 {
			_, _ = a.sendMessage(ctx, chatID, "Format: /set key value\nContoh: /set risk.max_position_idr 10000", nil)
			return
		}
		key := parts[1]
		value := strings.Join(parts[2:], " ")
		if err := a.store.SetSetting(ctx, key, value); err != nil {
			_, _ = a.sendMessage(ctx, chatID, "Error: "+err.Error(), nil)
			return
		}
		_ = a.reloadConfig(ctx)
		_, _ = a.sendMessage(ctx, chatID, "✅ Setting updated: "+key+" = "+value, nil)
	case "/mode":
		if len(parts) < 2 {
			_, _ = a.sendMessage(ctx, chatID, "Format: /mode paper atau /mode real", nil)
			return
		}
		mode := strings.ToLower(parts[1])
		if mode != "paper" && mode != "real" {
			_, _ = a.sendMessage(ctx, chatID, "Mode harus paper/real.", nil)
			return
		}
		_ = a.store.SetSetting(ctx, "trading.mode", mode)
		_ = a.reloadConfig(ctx)
		_, _ = a.sendMessage(ctx, chatID, "✅ Mode: "+mode, nil)
	case "/autobuy_paper":
		a.setOnOff(ctx, chatID, parts, "trading.allow_autobuy_paper")
	case "/autobuy_real":
		a.setOnOff(ctx, chatID, parts, "trading.allow_autobuy_real")
	case "/pause":
		a.mu.Lock()
		a.paused = true
		a.lastAction = "Paused by command."
		a.mu.Unlock()
		_, _ = a.sendMessage(ctx, chatID, "⏸ Paused.", nil)
	case "/resume":
		a.mu.Lock()
		a.paused = false
		a.lastAction = "Resumed by command."
		a.mu.Unlock()
		_, _ = a.sendMessage(ctx, chatID, "▶️ Resumed.", nil)
	case "/scan":
		_, _ = a.sendMessage(ctx, chatID, "Scanning sekali...", nil)
		go a.scanOnce(context.Background())
	case "/buy_paper":
		if err := a.buyCandidate(ctx, "paper"); err != nil {
			_, _ = a.sendMessage(ctx, chatID, "Error: "+err.Error(), nil)
			return
		}
		_, _ = a.sendMessage(ctx, chatID, "✅ Paper buy executed.", nil)
	case "/buy_real":
		if err := a.buyCandidate(ctx, "real"); err != nil {
			_, _ = a.sendMessage(ctx, chatID, "Error: "+err.Error(), nil)
			return
		}
		_, _ = a.sendMessage(ctx, chatID, "✅ Real buy executed.", nil)
	case "/sell_now", "/stop_holding":
		if err := a.sellCurrent(ctx, "manual stop"); err != nil {
			_, _ = a.sendMessage(ctx, chatID, "Error: "+err.Error(), nil)
			return
		}
		_, _ = a.sendMessage(ctx, chatID, "✅ Sell/stop executed.", nil)
	default:
		_, _ = a.sendMessage(ctx, chatID, "Command tidak dikenal. Ketik /help", nil)
	}
}

func (a *App) setOnOff(ctx context.Context, chatID int64, parts []string, key string) {
	if len(parts) < 2 {
		_, _ = a.sendMessage(ctx, chatID, "Format: "+parts[0]+" on/off", nil)
		return
	}
	v := strings.ToLower(parts[1])
	if v != "on" && v != "off" {
		_, _ = a.sendMessage(ctx, chatID, "Pakai on/off.", nil)
		return
	}
	val := "false"
	if v == "on" {
		val = "true"
	}
	_ = a.store.SetSetting(ctx, key, val)
	_ = a.reloadConfig(ctx)
	_, _ = a.sendMessage(ctx, chatID, "✅ "+key+" = "+val, nil)
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
