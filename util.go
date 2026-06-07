package main

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"
)

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
