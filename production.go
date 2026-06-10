package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

var ErrGeminiDeferred = errors.New("gemini deferred by local quota guard")

type IntervalLimiter struct {
	mu       sync.Mutex
	next     time.Time
	interval time.Duration
}

func NewIntervalLimiter(interval time.Duration) *IntervalLimiter {
	if interval <= 0 {
		interval = time.Millisecond
	}

	return &IntervalLimiter{
		interval: interval,
	}
}

func (l *IntervalLimiter) Wait(ctx context.Context) error {
	if l == nil {
		return nil
	}

	l.mu.Lock()

	now := time.Now()
	slot := now

	if l.next.After(now) {
		slot = l.next
	}

	l.next = slot.Add(l.interval)
	wait := time.Until(slot)

	l.mu.Unlock()

	if wait <= 0 {
		return nil
	}

	timer := time.NewTimer(wait)
	defer timer.Stop()

	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

type APIError struct {
	Service    string
	StatusCode int
	Body       string
	RetryAfter time.Duration
}

func (e *APIError) Error() string {
	return fmt.Sprintf(
		"%s HTTP %d: %s",
		e.Service,
		e.StatusCode,
		e.Body,
	)
}

func newAPIError(service string, resp *http.Response, body []byte) *APIError {
	return &APIError{
		Service:    service,
		StatusCode: resp.StatusCode,
		Body:       truncateText(string(body), 500),
		RetryAfter: parseRetryAfter(resp.Header.Get("Retry-After")),
	}
}

func parseRetryAfter(value string) time.Duration {
	value = strings.TrimSpace(value)

	if value == "" {
		return 0
	}

	if seconds, err := strconv.Atoi(value); err == nil && seconds > 0 {
		return time.Duration(seconds) * time.Second
	}

	if retryTime, err := http.ParseTime(value); err == nil {
		wait := time.Until(retryTime)

		if wait > 0 {
			return wait
		}
	}

	return 0
}

func sleepContext(ctx context.Context, duration time.Duration) error {
	if duration <= 0 {
		return nil
	}

	timer := time.NewTimer(duration)
	defer timer.Stop()

	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func truncateText(value string, max int) string {
	if len(value) <= max {
		return value
	}

	return value[:max] + "..."
}

func (a *App) beginScan(parent context.Context) (
	context.Context,
	Config,
	uint64,
	bool,
) {
	a.mu.Lock()
	defer a.mu.Unlock()

	if a.paused {
		a.lastAction = "Skip scan: scanner sedang paused."
		return nil, Config{}, 0, false
	}

	if a.position != nil && a.position.Status == "open" {
		a.lastAction = "Skip scan: masih holding 1 token."
		return nil, Config{}, 0, false
	}

	if a.candidate != nil {
		a.lastAction = "Skip scan: candidate masih menunggu aksi."
		return nil, Config{}, 0, false
	}

	if a.scanRunning {
		a.lastAction = "Skip scan: scan sebelumnya masih berjalan."
		return nil, Config{}, 0, false
	}

	scanContext, cancel := context.WithCancel(parent)

	a.scanRunning = true
	a.scanCancel = cancel
	a.scanSeq++
	a.lastScanAt = time.Now()
	a.lastAction = "Scanning candidates..."

	return scanContext, a.cfg, a.scanSeq, true
}

func (a *App) endScan(scanID uint64) {
	a.mu.Lock()
	defer a.mu.Unlock()

	if a.scanSeq != scanID {
		return
	}

	if a.scanCancel != nil {
		a.scanCancel()
	}

	a.scanCancel = nil
	a.scanRunning = false
}

func (a *App) scanStillValid(scanID uint64) bool {
	a.mu.Lock()
	defer a.mu.Unlock()

	if !a.scanRunning || a.scanSeq != scanID {
		return false
	}

	if a.paused {
		return false
	}

	if a.position != nil && a.position.Status == "open" {
		return false
	}

	if a.candidate != nil {
		return false
	}

	return true
}

func (a *App) reserveGeminiWindow(cfg Config) (bool, string) {
	a.mu.Lock()
	defer a.mu.Unlock()

	nowTime := time.Now()

	if nowTime.Before(a.aiGlobalCooldownTill) {
		return false, fmt.Sprintf(
			"Gemini cooldown sampai %s",
			a.aiGlobalCooldownTill.Format("15:04:05"),
		)
	}

	minimumInterval := time.Duration(
		cfg.AIMinIntervalSeconds,
	) * time.Second

	if !a.lastAIAt.IsZero() {
		elapsed := nowTime.Sub(a.lastAIAt)

		if elapsed < minimumInterval {
			remaining := minimumInterval - elapsed

			return false, fmt.Sprintf(
				"Gemini local cooldown %.0f detik",
				remaining.Seconds(),
			)
		}
	}

	a.lastAIAt = nowTime

	return true, ""
}

func (a *App) setGlobalGeminiCooldown(duration time.Duration) {
	if duration <= 0 {
		return
	}

	a.mu.Lock()
	defer a.mu.Unlock()

	until := time.Now().Add(duration)

	if until.After(a.aiGlobalCooldownTill) {
		a.aiGlobalCooldownTill = until
	}
}

func (a *App) setModelCooldown(model string, duration time.Duration) {
	if model == "" || duration <= 0 {
		return
	}

	a.mu.Lock()
	defer a.mu.Unlock()

	if a.modelCooldown == nil {
		a.modelCooldown = make(map[string]time.Time)
	}

	a.modelCooldown[model] = time.Now().Add(duration)
}

func (a *App) modelIsCoolingDown(model string) bool {
	a.mu.Lock()
	defer a.mu.Unlock()

	until, exists := a.modelCooldown[model]

	if !exists {
		return false
	}

	if time.Now().After(until) {
		delete(a.modelCooldown, model)
		return false
	}

	return true
}

func (a *App) waitDex(ctx context.Context) error {
	a.mu.Lock()
	limiter := a.dexLimiter
	a.mu.Unlock()

	return limiter.Wait(ctx)
}

func (a *App) waitGemini(ctx context.Context) error {
	a.mu.Lock()
	limiter := a.geminiLimiter
	a.mu.Unlock()

	return limiter.Wait(ctx)
}

func (a *App) waitJupiter(ctx context.Context) error {
	a.mu.Lock()
	limiter := a.jupiterLimiter
	a.mu.Unlock()

	return limiter.Wait(ctx)
}

func (s *Store) MigrateProduction(ctx context.Context) error {
	queries := []string{
		`CREATE TABLE IF NOT EXISTS api_usage (
			service TEXT NOT NULL,
			usage_date TEXT NOT NULL,
			request_count INTEGER NOT NULL DEFAULT 0,
			updated_at TEXT NOT NULL,
			PRIMARY KEY(service, usage_date)
		);`,

		`CREATE TABLE IF NOT EXISTS ai_cache (
			mint TEXT PRIMARY KEY,
			result_json TEXT NOT NULL,
			expires_at TEXT NOT NULL,
			updated_at TEXT NOT NULL
		);`,

		`CREATE TABLE IF NOT EXISTS candidate_cooldowns (
			mint TEXT PRIMARY KEY,
			until_at TEXT NOT NULL,
			reason TEXT,
			updated_at TEXT NOT NULL
		);`,
	}

	for _, query := range queries {
		if _, err := s.db.ExecContext(ctx, query); err != nil {
			return err
		}
	}

	s.syncDB.Push(ctx)

	return nil
}

func (s *Store) SeedProductionDefaults(ctx context.Context) error {
	current, err := s.GetAllSettings(ctx)
	if err != nil {
		return err
	}

	defaults := map[string]string{
		"scanner.candidate_cooldown_minutes": "30",

		"ai.min_interval_seconds":        "120",
		"ai.daily_max_calls":             "25",
		"ai.cache_ttl_minutes":           "30",
		"ai.max_attempts_per_analysis":   "2",
		"ai.rate_limit_cooldown_minutes": "60",

		"api.dex_min_interval_ms":     "1100",
		"api.gemini_min_interval_ms":  "1100",
		"api.jupiter_min_interval_ms": "1100",
	}

	inserted := false

	for key, value := range defaults {
		if _, exists := current[key]; exists {
			continue
		}

		_, err := s.db.ExecContext(
			ctx,
			`INSERT INTO settings(key, value, updated_at)
			 VALUES(?, ?, ?)`,
			key,
			value,
			now(),
		)
		if err != nil {
			return err
		}

		inserted = true
	}

	if inserted {
		s.syncDB.Push(ctx)
	}

	return nil
}

func (s *Store) GetAPIUsage(
	ctx context.Context,
	service string,
	date string,
) (int, error) {
	var count int

	err := s.db.QueryRowContext(
		ctx,
		`SELECT request_count
		   FROM api_usage
		  WHERE service = ? AND usage_date = ?`,
		service,
		date,
	).Scan(&count)

	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}

	return count, err
}

func (s *Store) IncrementAPIUsage(
	ctx context.Context,
	service string,
	date string,
	amount int,
) (int, error) {
	if amount <= 0 {
		amount = 1
	}

	_, err := s.db.ExecContext(
		ctx,
		`INSERT INTO api_usage(
			service,
			usage_date,
			request_count,
			updated_at
		)
		VALUES(?, ?, ?, ?)
		ON CONFLICT(service, usage_date)
		DO UPDATE SET
			request_count = request_count + excluded.request_count,
			updated_at = excluded.updated_at`,
		service,
		date,
		amount,
		now(),
	)
	if err != nil {
		return 0, err
	}

	s.syncDB.Push(ctx)

	return s.GetAPIUsage(ctx, service, date)
}

func (s *Store) LoadAICache(
	ctx context.Context,
	mint string,
) (*AIResult, bool, error) {
	var rawJSON string
	var expiresAt string

	err := s.db.QueryRowContext(
		ctx,
		`SELECT result_json, expires_at
		   FROM ai_cache
		  WHERE mint = ?`,
		mint,
	).Scan(&rawJSON, &expiresAt)

	if errors.Is(err, sql.ErrNoRows) {
		return nil, false, nil
	}

	if err != nil {
		return nil, false, err
	}

	expiry, err := time.Parse(time.RFC3339, expiresAt)
	if err != nil || time.Now().After(expiry) {
		return nil, false, nil
	}

	var result AIResult

	if err := json.Unmarshal([]byte(rawJSON), &result); err != nil {
		return nil, false, err
	}

	return &result, true, nil
}

func (s *Store) SaveAICache(
	ctx context.Context,
	mint string,
	result *AIResult,
	ttl time.Duration,
) error {
	if result == nil {
		return errors.New("AI result kosong")
	}

	rawJSON, err := json.Marshal(result)
	if err != nil {
		return err
	}

	expiresAt := time.Now().Add(ttl).Format(time.RFC3339)

	_, err = s.db.ExecContext(
		ctx,
		`INSERT INTO ai_cache(
			mint,
			result_json,
			expires_at,
			updated_at
		)
		VALUES(?, ?, ?, ?)
		ON CONFLICT(mint)
		DO UPDATE SET
			result_json = excluded.result_json,
			expires_at = excluded.expires_at,
			updated_at = excluded.updated_at`,
		mint,
		string(rawJSON),
		expiresAt,
		now(),
	)
	if err == nil {
		s.syncDB.Push(ctx)
	}

	return err
}

func (s *Store) IsCandidateCoolingDown(
	ctx context.Context,
	mint string,
) (bool, error) {
	var untilAt string

	err := s.db.QueryRowContext(
		ctx,
		`SELECT until_at
		   FROM candidate_cooldowns
		  WHERE mint = ?`,
		mint,
	).Scan(&untilAt)

	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}

	if err != nil {
		return false, err
	}

	until, err := time.Parse(time.RFC3339, untilAt)
	if err != nil {
		return false, nil
	}

	return time.Now().Before(until), nil
}

func (s *Store) SetCandidateCooldown(
	ctx context.Context,
	mint string,
	duration time.Duration,
	reason string,
) error {
	until := time.Now().Add(duration).Format(time.RFC3339)

	_, err := s.db.ExecContext(
		ctx,
		`INSERT INTO candidate_cooldowns(
			mint,
			until_at,
			reason,
			updated_at
		)
		VALUES(?, ?, ?, ?)
		ON CONFLICT(mint)
		DO UPDATE SET
			until_at = excluded.until_at,
			reason = excluded.reason,
			updated_at = excluded.updated_at`,
		mint,
		until,
		reason,
		now(),
	)
	if err == nil {
		s.syncDB.Push(ctx)
	}

	return err
}
