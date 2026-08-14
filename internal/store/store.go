// Package store persists chat settings and subscriptions in SQLite.
package store

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"time"

	_ "modernc.org/sqlite"
)

// Frequency of a digest subscription.
const (
	FrequencyDaily  = "daily"
	FrequencyWeekly = "weekly"
)

// Settings holds per-chat preferences.
type Settings struct {
	ChatID     string
	ChatType   string // p2p or group
	SummaryOn  bool
	Subscribed bool
	Frequency  string // daily or weekly, empty when not subscribed
	PushHour   int
	PushMinute int
	LastPushAt time.Time
}

// Store wraps the SQLite database.
type Store struct {
	db *sql.DB
}

// Open opens (creating if needed) the database at path.
func Open(path string) (*Store, error) {
	if dir := filepath.Dir(path); dir != "." && dir != "" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, fmt.Errorf("create data dir: %w", err)
		}
	}
	dsn := "file:" + path + "?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}
	db.SetMaxOpenConns(1) // sqlite writes are serialized
	s := &Store{db: db}
	if err := s.migrate(); err != nil {
		return nil, err
	}
	return s, nil
}

// Close closes the database.
func (s *Store) Close() error {
	return s.db.Close()
}

func (s *Store) migrate() error {
	const ddl = `
CREATE TABLE IF NOT EXISTS settings (
    chat_id       TEXT PRIMARY KEY,
    chat_type     TEXT NOT NULL DEFAULT 'p2p',
    summary_on    INTEGER NOT NULL DEFAULT 1,
    updated_at    INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS subscriptions (
    chat_id      TEXT PRIMARY KEY,
    frequency    TEXT NOT NULL CHECK (frequency IN ('daily','weekly')),
    push_hour    INTEGER NOT NULL,
    push_minute  INTEGER NOT NULL DEFAULT 0,
    enabled      INTEGER NOT NULL DEFAULT 1,
    last_push_at INTEGER NOT NULL DEFAULT 0,
    updated_at   INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS push_log (
    id           INTEGER PRIMARY KEY AUTOINCREMENT,
    chat_id      TEXT NOT NULL,
    period_start INTEGER NOT NULL,
    period_end   INTEGER NOT NULL,
    created_at   INTEGER NOT NULL,
    UNIQUE (chat_id, period_start, period_end)
);
`
	_, err := s.db.Exec(ddl)
	if err != nil {
		return fmt.Errorf("migrate: %w", err)
	}
	return nil
}

// GetSettings returns settings of a chat, creating defaults if absent.
func (s *Store) GetSettings(chatID, chatType string) (*Settings, error) {
	_, err := s.db.Exec(
		`INSERT OR IGNORE INTO settings (chat_id, chat_type, summary_on, updated_at) VALUES (?, ?, 1, ?)`,
		chatID, chatType, time.Now().Unix())
	if err != nil {
		return nil, fmt.Errorf("ensure settings: %w", err)
	}

	st := &Settings{ChatID: chatID, SummaryOn: true}
	var summaryOn int
	err = s.db.QueryRow(
		`SELECT chat_type, summary_on FROM settings WHERE chat_id = ?`, chatID,
	).Scan(&st.ChatType, &summaryOn)
	if err != nil {
		return nil, fmt.Errorf("query settings: %w", err)
	}
	st.SummaryOn = summaryOn != 0

	var (
		frequency             sql.NullString
		hour, minute, enabled int
		lastPush              int64
	)
	err = s.db.QueryRow(`
SELECT s.frequency, s.push_hour, s.push_minute, s.enabled, s.last_push_at
FROM subscriptions s WHERE s.chat_id = ? AND s.enabled = 1`, chatID,
	).Scan(&frequency, &hour, &minute, &enabled, &lastPush)
	if err == nil {
		st.Subscribed = true
		st.Frequency = frequency.String
		st.PushHour = hour
		st.PushMinute = minute
		st.LastPushAt = time.Unix(lastPush, 0)
	} else if err != sql.ErrNoRows {
		return nil, fmt.Errorf("query subscription: %w", err)
	}
	return st, nil
}

// SetSummary toggles the per-chat LLM summary of search results.
func (s *Store) SetSummary(chatID string, on bool) error {
	_, err := s.db.Exec(
		`UPDATE settings SET summary_on = ?, updated_at = ? WHERE chat_id = ?`,
		boolToInt(on), time.Now().Unix(), chatID)
	if err != nil {
		return fmt.Errorf("update summary: %w", err)
	}
	return nil
}

// UpsertSubscription inserts or updates a subscription of a chat.
func (s *Store) UpsertSubscription(chatID, frequency string, hour, minute int) error {
	_, err := s.db.Exec(`
INSERT INTO subscriptions (chat_id, frequency, push_hour, push_minute, enabled, updated_at)
VALUES (?, ?, ?, ?, 1, ?)
ON CONFLICT(chat_id) DO UPDATE SET
    frequency = excluded.frequency,
    push_hour = excluded.push_hour,
    push_minute = excluded.push_minute,
    enabled = 1,
    updated_at = excluded.updated_at`,
		chatID, frequency, hour, minute, time.Now().Unix())
	if err != nil {
		return fmt.Errorf("upsert subscription: %w", err)
	}
	return nil
}

// Unsubscribe disables the subscription of a chat.
func (s *Store) Unsubscribe(chatID string) error {
	_, err := s.db.Exec(
		`UPDATE subscriptions SET enabled = 0, updated_at = ? WHERE chat_id = ?`,
		time.Now().Unix(), chatID)
	if err != nil {
		return fmt.Errorf("unsubscribe: %w", err)
	}
	return nil
}

// TouchLastPush refills last_push_at without writing a push_log row, used
// when a slot was already pushed (e.g. after a restart).
func (s *Store) TouchLastPush(chatID string) error {
	_, err := s.db.Exec(
		`UPDATE subscriptions SET last_push_at = ? WHERE chat_id = ?`,
		time.Now().Unix(), chatID)
	if err != nil {
		return fmt.Errorf("touch last push: %w", err)
	}
	return nil
}

// MarkPushed records a successful push and its covered period.
func (s *Store) MarkPushed(chatID string, periodStart, periodEnd time.Time) error {
	now := time.Now()
	_, err := s.db.Exec(
		`INSERT OR IGNORE INTO push_log (chat_id, period_start, period_end, created_at) VALUES (?, ?, ?, ?)`,
		chatID, periodStart.Unix(), periodEnd.Unix(), now.Unix())
	if err != nil {
		return fmt.Errorf("insert push log: %w", err)
	}
	_, err = s.db.Exec(
		`UPDATE subscriptions SET last_push_at = ? WHERE chat_id = ?`,
		now.Unix(), chatID)
	if err != nil {
		return fmt.Errorf("update last push: %w", err)
	}
	return nil
}

// WasPushed reports whether the period was already pushed to the chat.
func (s *Store) WasPushed(chatID string, periodStart, periodEnd time.Time) (bool, error) {
	var n int
	err := s.db.QueryRow(
		`SELECT COUNT(1) FROM push_log WHERE chat_id = ? AND period_start = ? AND period_end = ?`,
		chatID, periodStart.Unix(), periodEnd.Unix()).Scan(&n)
	if err != nil {
		return false, fmt.Errorf("query push log: %w", err)
	}
	return n > 0, nil
}

// ActiveSubscriptions returns all enabled subscriptions.
func (s *Store) ActiveSubscriptions() ([]*Settings, error) {
	rows, err := s.db.Query(`
SELECT s.chat_id, st.chat_type, s.frequency, s.push_hour, s.push_minute, s.last_push_at
FROM subscriptions s
LEFT JOIN settings st ON st.chat_id = s.chat_id
WHERE s.enabled = 1`)
	if err != nil {
		return nil, fmt.Errorf("query subscriptions: %w", err)
	}
	defer rows.Close()

	var ret []*Settings
	for rows.Next() {
		var (
			chatID, chatType, frequency string
			hour, minute                int
			lastPush                    int64
		)
		if err := rows.Scan(&chatID, &chatType, &frequency, &hour, &minute, &lastPush); err != nil {
			return nil, fmt.Errorf("scan subscription: %w", err)
		}
		if chatType == "" {
			chatType = "p2p"
		}
		ret = append(ret, &Settings{
			ChatID:     chatID,
			ChatType:   chatType,
			Subscribed: true,
			Frequency:  frequency,
			PushHour:   hour,
			PushMinute: minute,
			LastPushAt: time.Unix(lastPush, 0),
		})
	}
	return ret, rows.Err()
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
