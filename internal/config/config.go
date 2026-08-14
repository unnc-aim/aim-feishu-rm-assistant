// Package config loads runtime configuration from environment variables.
package config

import (
	"os"
	"strconv"
)

// Config holds all runtime configuration of the assistant.
type Config struct {
	// AppID and AppSecret are the Feishu open platform app credentials.
	AppID     string
	AppSecret string

	// RmSearchBaseURL is the base URL of the rm-search deployment.
	RmSearchBaseURL string

	// LLMBaseURL, LLMAPIKey and LLMModel configure an OpenAI-compatible
	// chat completion API used for summarization only.
	LLMBaseURL string
	LLMAPIKey  string
	LLMModel   string

	// SQLitePath is the file path of the SQLite database.
	SQLitePath string

	// LogDir is the directory holding daily-rotated log files; every
	// incoming user message and action is logged there in addition to
	// stderr.
	LogDir string

	// PushDefaultHour and PushDefaultMinute are the local time-of-day used
	// when a new subscription does not specify one.
	PushDefaultHour   int
	PushDefaultMinute int
}

// FromEnv builds a Config from environment variables with sane defaults.
func FromEnv() *Config {
	return &Config{
		AppID:             os.Getenv("FEISHU_APP_ID"),
		AppSecret:         os.Getenv("FEISHU_APP_SECRET"),
		RmSearchBaseURL:   envOr("RMSEARCH_BASE_URL", "https://search.scutbot.cn"),
		LLMBaseURL:        envOr("LLM_BASE_URL", "https://api.openai.com/v1"),
		LLMAPIKey:         os.Getenv("LLM_API_KEY"),
		LLMModel:          envOr("LLM_MODEL", "gpt-4o-mini"),
		SQLitePath:        envOr("SQLITE_PATH", "./data/assistant.db"),
		LogDir:            envOr("LOG_DIR", "./data/logs"),
		PushDefaultHour:   envInt("PUSH_DEFAULT_HOUR", 20),
		PushDefaultMinute: envInt("PUSH_DEFAULT_MINUTE", 0),
	}
}

// Validate reports whether mandatory fields are present.
func (c *Config) Validate() error {
	if c.AppID == "" || c.AppSecret == "" {
		return &MissingFieldError{Fields: []string{"FEISHU_APP_ID", "FEISHU_APP_SECRET"}}
	}
	return nil
}

// MissingFieldError indicates one or more required environment variables are unset.
type MissingFieldError struct {
	Fields []string
}

func (e *MissingFieldError) Error() string {
	msg := "missing required environment variables:"
	for _, f := range e.Fields {
		msg += " " + f
	}
	return msg
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func envInt(key string, fallback int) int {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return fallback
}
