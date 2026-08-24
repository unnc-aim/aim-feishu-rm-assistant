package push

import (
	"testing"
	"time"

	"github.com/unnc-aim/aim-feishu-rm-assistant/internal/store"
)

func dailySub() *store.Settings {
	return &store.Settings{Frequency: store.FrequencyDaily, PushHour: 20}
}
func weeklySub() *store.Settings {
	return &store.Settings{Frequency: store.FrequencyWeekly, PushHour: 9}
}

func TestSlotOfDaily(t *testing.T) {
	loc := time.FixedZone("test", 8*3600)
	// Saturday 2026-08-15 21:00, slot should be today 20:00.
	now := time.Date(2026, 8, 15, 21, 0, 0, 0, loc)
	slot := slotOf(dailySub(), now)
	want := time.Date(2026, 8, 15, 20, 0, 0, 0, loc)
	if !slot.Equal(want) {
		t.Errorf("slot = %v, want %v", slot, want)
	}
	// Before today's slot, the slot is yesterday's.
	now = time.Date(2026, 8, 15, 10, 0, 0, 0, loc)
	slot = slotOf(dailySub(), now)
	want = time.Date(2026, 8, 14, 20, 0, 0, 0, loc)
	if !slot.Equal(want) {
		t.Errorf("slot = %v, want %v", slot, want)
	}
}

func TestSlotOfWeekly(t *testing.T) {
	loc := time.FixedZone("test", 8*3600)
	// 2026-08-15 is a Saturday; the Monday slot is 2026-08-10 09:00.
	now := time.Date(2026, 8, 15, 12, 0, 0, 0, loc)
	slot := slotOf(weeklySub(), now)
	want := time.Date(2026, 8, 10, 9, 0, 0, 0, loc)
	if !slot.Equal(want) {
		t.Errorf("slot = %v, want %v", slot, want)
	}
	// Monday 08:00 is before this week's slot: previous Monday.
	now = time.Date(2026, 8, 10, 8, 0, 0, 0, loc)
	slot = slotOf(weeklySub(), now)
	want = time.Date(2026, 8, 3, 9, 0, 0, 0, loc)
	if !slot.Equal(want) {
		t.Errorf("slot = %v, want %v", slot, want)
	}
	// Sunday counts as the 7th day of the same week.
	now = time.Date(2026, 8, 16, 10, 0, 0, 0, loc)
	slot = slotOf(weeklySub(), now)
	if !slot.Equal(want.AddDate(0, 0, 7)) {
		t.Errorf("slot = %v, want %v", slot, want.AddDate(0, 0, 7))
	}
}

func TestWindowOf(t *testing.T) {
	loc := time.FixedZone("test", 8*3600)
	slot := time.Date(2026, 8, 10, 9, 0, 0, 0, loc)

	start, end := windowOf(weeklySub(), slot)
	if start != time.Date(2026, 8, 3, 0, 0, 0, 0, loc) {
		t.Errorf("weekly start = %v", start)
	}
	if end != time.Date(2026, 8, 10, 0, 0, 0, 0, loc) {
		t.Errorf("weekly end = %v", end)
	}

	start, end = windowOf(dailySub(), slot)
	if end.Sub(start) != 24*time.Hour {
		t.Errorf("daily window = %v ~ %v", start, end)
	}
}

func monthlySub() *store.Settings {
	return &store.Settings{Frequency: store.FrequencyMonthly, PushHour: 9}
}

func TestSlotOfAndWindowOfMonthly(t *testing.T) {
	loc := time.FixedZone("test", 8*3600)
	// 2026-08-24 is mid-month; the slot is Aug 1st 09:00, the window
	// covers July (07-01 00:00 ~ 08-01 00:00).
	now := time.Date(2026, 8, 24, 12, 0, 0, 0, loc)
	slot := slotOf(monthlySub(), now)
	want := time.Date(2026, 8, 1, 9, 0, 0, 0, loc)
	if !slot.Equal(want) {
		t.Fatalf("monthly slot = %v, want %v", slot, want)
	}
	start, end := windowOf(monthlySub(), slot)
	if !start.Equal(time.Date(2026, 7, 1, 0, 0, 0, 0, loc)) {
		t.Errorf("monthly window start = %v", start)
	}
	if !end.Equal(time.Date(2026, 8, 1, 0, 0, 0, 0, loc)) {
		t.Errorf("monthly window end = %v", end)
	}

	// Before this month's slot (Aug 1st 08:00): slot falls back to July 1st.
	early := time.Date(2026, 8, 1, 8, 0, 0, 0, loc)
	if got := slotOf(monthlySub(), early); !got.Equal(time.Date(2026, 7, 1, 9, 0, 0, 0, loc)) {
		t.Errorf("early-month slot = %v", got)
	}

	// nextFire: mid-month -> the 1st of next month.
	if got := nextFire(monthlySub(), now); !got.Equal(time.Date(2026, 9, 1, 9, 0, 0, 0, loc)) {
		t.Errorf("nextFire = %v, want 2026-09-01 09:00", got)
	}
}
