package log

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sirupsen/logrus"
)

func TestDailyWriterRotatesPerDay(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, 8, 15, 10, 0, 0, 0, time.Local)
	w := &dailyWriter{dir: dir, now: func() time.Time { return now }}

	if _, err := w.Write([]byte("day1 line\n")); err != nil {
		t.Fatalf("write day1: %v", err)
	}

	// Same day: same file.
	if _, err := w.Write([]byte("day1 line2\n")); err != nil {
		t.Fatalf("write day1 again: %v", err)
	}

	// Next day: rotation to a new file.
	now = now.AddDate(0, 0, 1)
	if _, err := w.Write([]byte("day2 line\n")); err != nil {
		t.Fatalf("write day2: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	day1, err := os.ReadFile(filepath.Join(dir, "assistant-2026-08-15.log"))
	if err != nil {
		t.Fatalf("read day1: %v", err)
	}
	if string(day1) != "day1 line\nday1 line2\n" {
		t.Errorf("day1 content = %q", day1)
	}

	day2, err := os.ReadFile(filepath.Join(dir, "assistant-2026-08-16.log"))
	if err != nil {
		t.Fatalf("read day2: %v", err)
	}
	if string(day2) != "day2 line\n" {
		t.Errorf("day2 content = %q", day2)
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 {
		t.Errorf("files in dir = %d, want 2", len(entries))
	}
	for _, e := range entries {
		if !strings.HasPrefix(e.Name(), "assistant-") || !strings.HasSuffix(e.Name(), ".log") {
			t.Errorf("unexpected file name %q", e.Name())
		}
	}
}

func TestSetupCreatesDirAndWrites(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "nested", "logs")
	closer, err := Setup(dir)
	if err != nil {
		t.Fatalf("setup: %v", err)
	}
	t.Cleanup(func() { _ = closer.Close() })

	info := []byte("hello log\n")
	if n, err := writeViaLogrus(info); err != nil || n != len(info) {
		t.Fatalf("write via logrus output: %d, %v", n, err)
	}

	matches, _ := filepath.Glob(filepath.Join(dir, "assistant-*.log"))
	if len(matches) != 1 {
		t.Fatalf("log files = %v, want exactly one", matches)
	}
	data, err := os.ReadFile(matches[0])
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "hello log") {
		t.Errorf("file content %q missing written line", data)
	}
}

// writeViaLogrus writes through the logrus output configured by Setup.
func writeViaLogrus(p []byte) (int, error) {
	return logrus.StandardLogger().Out.Write(p)
}
