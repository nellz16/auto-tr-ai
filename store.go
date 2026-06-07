package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"time"

	turso "turso.tech/database/tursogo"
)

type Store struct {
	db     *sql.DB
	syncDB *turso.TursoSyncDb
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
		"trading.mode":                   "paper",
		"trading.allow_autobuy_paper":    "true",
		"trading.allow_autobuy_real":     "false",
		"trading.approval_required_real": "true",

		"risk.max_open_positions": "1",
		"risk.max_position_idr":   "10000",
		"risk.max_daily_loss_idr": "15000",
		"risk.max_daily_trades":   "3",

		"exit.stop_loss_pct":          "-20",
		"exit.take_profit_1_pct":      "35",
		"exit.take_profit_1_sell_pct": "50",
		"exit.take_profit_2_pct":      "70",
		"exit.trailing_start_pct":     "35",
		"exit.trailing_distance_pct":  "18",
		"exit.max_hold_minutes":       "45",

		"swap.slippage_bps": "700",

		"interval.scanner_seconds":       "35",
		"interval.position_seconds":      "10",
		"interval.telegram_edit_seconds": "6",

		"market.usd_idr": "16500",
		"market.sol_usd": "150",

		"scanner.max_profiles":            "25",
		"scanner.min_liquidity_usd":       "8000",
		"scanner.max_liquidity_usd":       "300000",
		"scanner.min_volume_5m_usd":       "3000",
		"scanner.min_txns_5m":             "30",
		"scanner.min_buys_5m":             "18",
		"scanner.max_sell_ratio_5m":       "0.55",
		"scanner.min_pair_age_minutes":    "10",
		"scanner.max_pair_age_hours":      "12",
		"scanner.max_price_change_5m_pct": "90",
		"scanner.max_price_change_1h_pct": "260",
		"scanner.min_score":               "70",

		"ai.enabled":                "true",
		"ai.min_confidence":         "0.70",
		"ai.model_priority":         "gemini-3.5-flash,gemini-3.1-flash-lite,gemini-3-flash-preview,gemini-2.5-flash,gemini-2.5-flash-lite",
		"ai.model_cooldown_minutes": "30",

		"api.dexscreener_base_url": DefaultDexScreenerBase,
		"api.gemini_base_url":      DefaultGeminiBaseURL,
		"api.jupiter_base_url":     DefaultJupiterBaseURL,
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
