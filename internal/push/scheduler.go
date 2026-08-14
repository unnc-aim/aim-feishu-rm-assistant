// Package push schedules and renders periodic digest pushes.
package push

import (
	"context"
	"time"

	"github.com/sirupsen/logrus"

	"github.com/unnc-aim/aim-feishu-rm-assistant/internal/bot"
	"github.com/unnc-aim/aim-feishu-rm-assistant/internal/store"
)

// Scheduler periodically checks subscriptions and sends digests.
type Scheduler struct {
	Bot   *bot.Bot
	Store *store.Store
}

// Start runs the scheduling loop until ctx is cancelled.
func (s *Scheduler) Start(ctx context.Context) {
	const interval = 30 * time.Second
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.tick(ctx)
		}
	}
}

// tick evaluates all active subscriptions once.
func (s *Scheduler) tick(ctx context.Context) {
	subs, err := s.Store.ActiveSubscriptions()
	if err != nil {
		logrus.Errorf("load subscriptions: %v", err)
		return
	}
	now := time.Now()
	for _, sub := range subs {
		if s.due(sub, now) {
			s.pushOne(ctx, sub, now)
		}
	}
}

// due reports whether the subscription should fire at now. A slot is due
// when now has passed the slot and no push happened at or after the slot.
func (s *Scheduler) due(sub *store.Settings, now time.Time) bool {
	slot := slotOf(sub, now)
	if now.Before(slot) {
		return false
	}
	return sub.LastPushAt.Before(slot)
}

// slotOf returns the most recent scheduled push time at or before now.
func slotOf(sub *store.Settings, now time.Time) time.Time {
	if sub.Frequency == store.FrequencyWeekly {
		// Fixed natural week: push on Monday at the configured time.
		weekday := int(now.Weekday())
		if weekday == 0 {
			weekday = 7
		}
		monday := now.AddDate(0, 0, -(weekday - 1))
		slot := time.Date(monday.Year(), monday.Month(), monday.Day(),
			sub.PushHour, sub.PushMinute, 0, 0, now.Location())
		if now.Before(slot) {
			// Before this Monday's slot, the previous slot was last week.
			return slot.AddDate(0, 0, -7)
		}
		return slot
	}
	slot := time.Date(now.Year(), now.Month(), now.Day(),
		sub.PushHour, sub.PushMinute, 0, 0, now.Location())
	if now.Before(slot) {
		return slot.AddDate(0, 0, -1)
	}
	return slot
}

// pushOne builds and sends the digest for one subscription, then records it.
func (s *Scheduler) pushOne(ctx context.Context, sub *store.Settings, now time.Time) {
	start, end := windowOf(sub, now)
	if done, err := s.Store.WasPushed(sub.ChatID, start, end); err != nil {
		logrus.Errorf("check push log: %v", err)
		return
	} else if done {
		// Refill last_push_at so due() stops firing for this slot.
		_ = s.markOnly(sub.ChatID)
		return
	}

	card, err := s.BuildDigest(ctx, start, end)
	if err != nil {
		logrus.Errorf("build digest for %s: %v", sub.ChatID, err)
		return
	}
	if err := s.Bot.SendCard(ctx, sub.ChatID, card); err != nil {
		logrus.Errorf("send digest to %s: %v", sub.ChatID, err)
		return
	}
	if err := s.Store.MarkPushed(sub.ChatID, start, end); err != nil {
		logrus.Errorf("mark pushed %s: %v", sub.ChatID, err)
	}
	logrus.Infof("digest pushed to %s (%s, %s ~ %s)", sub.ChatID, sub.Frequency, start, end)
}

func (s *Scheduler) markOnly(chatID string) error {
	// MarkPushed with the same window would violate the unique constraint,
	// so refill last_push_at without inserting a push_log row.
	return s.Store.TouchLastPush(chatID)
}

// windowOf returns the period a push covers.
// Daily: the 24 hours before the slot. Weekly: the previous natural week
// (Monday 00:00 to Sunday 24:00, i.e. the Monday slot minus 7 days).
func windowOf(sub *store.Settings, slot time.Time) (time.Time, time.Time) {
	if sub.Frequency == store.FrequencyWeekly {
		start := time.Date(slot.Year(), slot.Month(), slot.Day(), 0, 0, 0, 0, slot.Location()).AddDate(0, 0, -7)
		end := start.AddDate(0, 0, 7)
		return start, end
	}
	return slot.Add(-24 * time.Hour), slot
}
