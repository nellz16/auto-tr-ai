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
	"strings"
)

func (a *App) analyzeWithGemini(ctx context.Context, cfg Config, c *Candidate) (*AIResult, error) {
	payload := map[string]any{
		"token":               c.Symbol,
		"mint":                c.Mint,
		"pair_age_minutes":    c.PairAgeMin,
		"liquidity_usd":       c.LiquidityUSD,
		"volume_5m_usd":       c.Volume5mUSD,
		"buys_5m":             c.Buys5m,
		"sells_5m":            c.Sells5m,
		"sell_ratio_5m":       c.SellRatio5m,
		"price_change_5m_pct": c.PriceChange5m,
		"price_change_1h_pct": c.PriceChange1h,
		"score":               c.Score,
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
		Verdict:    "REJECT",
		Confidence: 0,
		Risk:       "HIGH",
		Reason:     "All Gemini models failed or returned invalid JSON.",
		ModelUsed:  "none",
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
