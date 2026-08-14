// Package log configures global file logging.
//
// Every log line (including each incoming user message, see the bot
// package) goes to both stderr and the configured log file so questions
// and actions are auditable on disk.
package log

import (
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/sirupsen/logrus"
)

// Setup redirects logrus output to stderr plus the file at path,
// creating parent directories as needed. The caller owns closing the file
// (use the returned closer).
func Setup(path string) (io.Closer, error) {
	if dir := filepath.Dir(path); dir != "." && dir != "" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, fmt.Errorf("create log dir: %w", err)
		}
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return nil, fmt.Errorf("open log file: %w", err)
	}

	logrus.SetOutput(io.MultiWriter(os.Stderr, f))
	logrus.SetFormatter(&logrus.TextFormatter{
		FullTimestamp:   true,
		TimestampFormat: "2006-01-02 15:04:05",
	})
	return f, nil
}
