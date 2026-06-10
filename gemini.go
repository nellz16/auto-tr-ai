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
	"time"
)

func (a *App) analyzeWithGemini(
	ctx context.Context,
	cfg Config,
	candidate *Candidate,
	scanID uint64,
) (*AIResult, error) {
	cached, found, err := a.store.LoadAICache(
		ctx,
		candidate.Mint,
	)
	if err != nil {
		return nil, err
	}

	if found {
		if cached.ModelUsed == "" {
			cached.ModelUsed = "cache"
		} else {
			cached.ModelUsed += " (cache)"
		}

		return cached, nil
	}

	if !a.scanStillValid(scanID) {
		return nil, context.Canceled
	}

	allowed, reason := a.reserveGeminiWindow(cfg)
	if !allowed {
		return nil, fmt.Errorf(
			"%w: %s",
			ErrGeminiDeferred,
			reason,
		)
	}

	usageDate := time.Now().UTC().Format("2006-01-02")

	currentUsage, err := a.store.GetAPIUsage(
		ctx,
		"gemini",
		usageDate,
	)
	if err != nil {
		return nil, err
	}

	if currentUsage >= cfg.AIDailyMaxCalls {
		return nil, fmt.Errorf(
			"%w: Gemini daily budget tercapai %d/%d",
			ErrGeminiDeferred,
			currentUsage,
			cfg.AIDailyMaxCalls,
		)
	}

	payload := map[string]any{
		"token":               candidate.Symbol,
		"mint":                candidate.Mint,
		"pair_age_minutes":    candidate.PairAgeMin,
		"liquidity_usd":       candidate.LiquidityUSD,
		"volume_5m_usd":       candidate.Volume5mUSD,
		"buys_5m":             candidate.Buys5m,
		"sells_5m":            candidate.Sells5m,
		"sell_ratio_5m":       candidate.SellRatio5m,
		"price_change_5m_pct": candidate.PriceChange5m,
		"price_change_1h_pct": candidate.PriceChange1h,
		"local_score":         candidate.Score,
	}

	payloadJSON, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}

	systemInstruction := `You are a conservative AI risk analyst for a Solana memecoin micro-trading bot.

You never execute trades.
You never choose wallet keys.
You only evaluate the supplied market data.
Reject when data is ambiguous, unusually volatile, manipulated, or insufficient.

Return only valid JSON:
{
  "verdict": "PASS or REJECT",
  "confidence": 0.0,
  "risk": "LOW, MEDIUM, or HIGH",
  "reason": "short explanation",
  "max_hold_minutes": 30
}`

	userPrompt := "Analyze this candidate:\n" +
		string(payloadJSON)

	attempts := 0
	var lastError error

	for _, model := range cfg.AIModelPriority {
		if attempts >= cfg.AIMaxAttemptsPerAnalysis {
			break
		}

		if !a.scanStillValid(scanID) {
			return nil, context.Canceled
		}

		if a.modelIsCoolingDown(model) {
			continue
		}

		usage, err := a.store.GetAPIUsage(
			ctx,
			"gemini",
			usageDate,
		)
		if err != nil {
			return nil, err
		}

		if usage >= cfg.AIDailyMaxCalls {
			return nil, fmt.Errorf(
				"%w: Gemini daily budget tercapai %d/%d",
				ErrGeminiDeferred,
				usage,
				cfg.AIDailyMaxCalls,
			)
		}

		attempts++

		result, callErr := a.callGemini(
			ctx,
			cfg,
			model,
			systemInstruction,
			userPrompt,
		)

		_, _ = a.store.IncrementAPIUsage(
			context.Background(),
			"gemini",
			usageDate,
			1,
		)

		if callErr == nil {
			result.ModelUsed = model

			if result.Verdict != "PASS" &&
				result.Verdict != "REJECT" {

				lastError = errors.New(
					"Gemini verdict bukan PASS/REJECT",
				)

				a.setModelCooldown(
					model,
					5*time.Minute,
				)

				continue
			}

			if result.Confidence < 0 {
				result.Confidence = 0
			}

			if result.Confidence > 1 {
				result.Confidence = 1
			}

			if result.MaxHoldMinutes <= 0 {
				result.MaxHoldMinutes = 30
			}

			_ = a.store.SaveAICache(
				context.Background(),
				candidate.Mint,
				result,
				time.Duration(
					cfg.AICacheTTLMinutes,
				)*time.Minute,
			)

			return result, nil
		}

		lastError = callErr

		var apiError *APIError

		if errors.As(callErr, &apiError) {
			switch apiError.StatusCode {
			case http.StatusTooManyRequests:
				cooldown := time.Duration(
					cfg.AIRateLimitCooldownMinutes,
				) * time.Minute

				if apiError.RetryAfter > cooldown {
					cooldown = apiError.RetryAfter
				}

				a.setGlobalGeminiCooldown(cooldown)
				a.setModelCooldown(model, cooldown)

				return nil, fmt.Errorf(
					"%w: Gemini 429, cooldown %.0f menit",
					ErrGeminiDeferred,
					cooldown.Minutes(),
				)

			case http.StatusNotFound:
				// Model mungkin dipensiunkan atau belum tersedia
				// untuk project ini. Lanjut ke fallback.
				a.setModelCooldown(
					model,
					24*time.Hour,
				)

				continue

			case http.StatusBadRequest,
				http.StatusUnauthorized,
				http.StatusForbidden:

				a.setGlobalGeminiCooldown(
					time.Duration(
						cfg.AIRateLimitCooldownMinutes,
					) * time.Minute,
				)

				return nil, fmt.Errorf(
					"%w: konfigurasi Gemini ditolak: %v",
					ErrGeminiDeferred,
					callErr,
				)

			default:
				if apiError.StatusCode >= 500 {
					a.setModelCooldown(
						model,
						5*time.Minute,
					)

					continue
				}

				return nil, callErr
			}
		}

		// Network error atau JSON response invalid.
		a.setModelCooldown(
			model,
			3*time.Minute,
		)
	}

	a.setGlobalGeminiCooldown(10 * time.Minute)

	if lastError == nil {
		lastError = errors.New(
			"tidak ada model Gemini tersedia",
		)
	}

	return nil, fmt.Errorf(
		"%w: semua model gagal: %v",
		ErrGeminiDeferred,
		lastError,
	)
}

func (a *App) callGemini(
	ctx context.Context,
	cfg Config,
	model string,
	systemInstruction string,
	userPrompt string,
) (*AIResult, error) {
	if err := a.waitGemini(ctx); err != nil {
		return nil, err
	}

	requestBody := GeminiRequest{
		SystemInstruction: &GeminiSystemInstruction{
			Parts: []GeminiPart{
				{Text: systemInstruction},
			},
		},

		Contents: []GeminiContent{
			{
				Parts: []GeminiPart{
					{Text: userPrompt},
				},
			},
		},

		GenerationConfig: GeminiGenerationConfig{
			ResponseMIMEType: "application/json",
			Temperature:      0.1,
			MaxOutputTokens:  256,
		},
	}

	rawBody, err := json.Marshal(requestBody)
	if err != nil {
		return nil, err
	}

	endpoint := fmt.Sprintf(
		"%s/models/%s:generateContent",
		strings.TrimRight(cfg.GeminiBaseURL, "/"),
		url.PathEscape(model),
	)

	request, err := http.NewRequestWithContext(
		ctx,
		http.MethodPost,
		endpoint,
		bytes.NewReader(rawBody),
	)
	if err != nil {
		return nil, err
	}

	request.Header.Set(
		"Content-Type",
		"application/json",
	)

	// Lebih aman daripada menaruh API key di query URL.
	request.Header.Set(
		"x-goog-api-key",
		a.env.GeminiAPIKey,
	)

	response, err := a.client.Do(request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()

	body, err := io.ReadAll(response.Body)
	if err != nil {
		return nil, err
	}

	if response.StatusCode < 200 ||
		response.StatusCode >= 300 {

		return nil, newAPIError(
			"gemini "+model,
			response,
			body,
		)
	}

	var geminiResponse GeminiResponse

	if err := json.Unmarshal(
		body,
		&geminiResponse,
	); err != nil {
		return nil, err
	}

	if geminiResponse.Error != nil {
		return nil, errors.New(
			geminiResponse.Error.Message,
		)
	}

	if len(geminiResponse.Candidates) == 0 ||
		len(
			geminiResponse.
				Candidates[0].
				Content.
				Parts,
		) == 0 {

		return nil, errors.New(
			"Gemini mengembalikan response kosong",
		)
	}

	text := strings.TrimSpace(
		geminiResponse.
			Candidates[0].
			Content.
			Parts[0].
			Text,
	)

	text = strings.TrimPrefix(text, "```json")
	text = strings.TrimPrefix(text, "```")
	text = strings.TrimSuffix(text, "```")
	text = strings.TrimSpace(text)

	var result AIResult

	if err := json.Unmarshal(
		[]byte(text),
		&result,
	); err != nil {
		return nil, fmt.Errorf(
			"invalid Gemini JSON: %w",
			err,
		)
	}

	result.Verdict = strings.ToUpper(
		strings.TrimSpace(result.Verdict),
	)

	result.Risk = strings.ToUpper(
		strings.TrimSpace(result.Risk),
	)

	return &result, nil
}
