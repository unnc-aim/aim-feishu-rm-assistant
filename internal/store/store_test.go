package store

import (
	"path/filepath"
	"testing"
	"time"
)

func newTestStore(t *testing.T) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func TestSettingsRoundtrip(t *testing.T) {
	s := newTestStore(t)

	st, err := s.GetSettings("chat1", "p2p")
	if err != nil {
		t.Fatalf("get settings: %v", err)
	}
	if !st.SummaryOn || st.Subscribed {
		t.Errorf("defaults wrong: %+v", st)
	}

	if err := s.SetSummary("chat1", false); err != nil {
		t.Fatalf("set summary: %v", err)
	}
	st, _ = s.GetSettings("chat1", "p2p")
	if st.SummaryOn {
		t.Error("summary should be off")
	}
}

func TestSubscriptionRoundtrip(t *testing.T) {
	s := newTestStore(t)

	if err := s.UpsertSubscription("chat1", FrequencyDaily, 21, 30); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	st, err := s.GetSettings("chat1", "p2p")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if !st.Subscribed || st.Frequency != FrequencyDaily || st.PushHour != 21 || st.PushMinute != 30 {
		t.Errorf("subscription wrong: %+v", st)
	}

	if err := s.Unsubscribe("chat1"); err != nil {
		t.Fatalf("unsubscribe: %v", err)
	}
	st, _ = s.GetSettings("chat1", "p2p")
	if st.Subscribed {
		t.Error("should be unsubscribed")
	}

	subs, err := s.ActiveSubscriptions()
	if err != nil {
		t.Fatalf("active: %v", err)
	}
	if len(subs) != 0 {
		t.Errorf("active count = %d, want 0", len(subs))
	}
}

func TestPushLogDedup(t *testing.T) {
	s := newTestStore(t)
	_ = s.UpsertSubscription("chat1", FrequencyWeekly, 20, 0)

	start := time.Date(2026, 8, 4, 0, 0, 0, 0, time.Local)
	end := start.AddDate(0, 0, 7)

	if err := s.MarkPushed("chat1", start, end); err != nil {
		t.Fatalf("mark: %v", err)
	}
	// Marking the same window twice must not fail (unique constraint).
	if err := s.MarkPushed("chat1", start, end); err != nil {
		t.Fatalf("mark twice: %v", err)
	}

	done, err := s.WasPushed("chat1", start, end)
	if err != nil || !done {
		t.Errorf("WasPushed = %v, %v; want true, nil", done, err)
	}
	done, _ = s.WasPushed("chat1", start.AddDate(0, 0, 7), end.AddDate(0, 0, 7))
	if done {
		t.Error("different window should not be pushed")
	}
}
