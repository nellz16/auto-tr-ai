package main

import (
	"encoding/json"
	"net/http"
	"time"
)

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
