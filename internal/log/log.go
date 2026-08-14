// Package log configures global file logging.
//
// Every log line (including each incoming user message, see the bot
// package) goes to both stderr and a daily-rotated file inside the
// configured directory (assistant-YYYY-MM-DD.log), so questions and
// actions are auditable on disk and each day starts a fresh file.
package log

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/sirupsen/logrus"
)

// filePrefix is the rotated file name prefix inside the directory.
const filePrefix = "assistant"

// dailyWriter appends to <dir>/assistant-YYYY-MM-DD.log, switching to a
// new file the first time a write happens on a later day. Rotation is
// lazy (no timers) and safe for concurrent use.
type dailyWriter struct {
	mu  sync.Mutex
	dir string
	day string
	f   *os.File
	now func() time.Time // injectable for tests
}

// Write implements io.Writer with daily rotation.
func (w *dailyWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()

	day := w.now().Format("2006-01-02")
	if w.f == nil || day != w.day {
		if err := w.rotate(day); err != nil {
			return 0, err
		}
	}
	return w.f.Write(p)
}

// rotate closes the current file (if any) and opens the file for day.
// Callers must hold w.mu.
func (w *dailyWriter) rotate(day string) error {
	if w.f != nil {
		_ = w.f.Close()
	}
	path := filepath.Join(w.dir, fmt.Sprintf("%s-%s.log", filePrefix, day))
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		// Keep pointing at the old state so the error surfaces per write.
		return fmt.Errorf("open log file %s: %w", path, err)
	}
	w.f = f
	w.day = day
	fmt.Fprintf(os.Stderr, "log file rotated to %s\n", path)
	return nil
}

// Close closes the current file.
func (w *dailyWriter) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.f == nil {
		return nil
	}
	err := w.f.Close()
	w.f = nil
	return err
}

// Setup redirects logrus output to stderr plus daily-rotated files in
// dir, creating the directory as needed. The caller owns the returned
// closer.
func Setup(dir string) (io.Closer, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("create log dir: %w", err)
	}

	w := &dailyWriter{dir: dir, now: time.Now}
	logrus.SetOutput(io.MultiWriter(os.Stderr, w))
	logrus.SetFormatter(&logrus.TextFormatter{
		FullTimestamp:   true,
		TimestampFormat: "2006-01-02 15:04:05",
	})
	return w, nil
}
