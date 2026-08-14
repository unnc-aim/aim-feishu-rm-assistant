// Package push schedules and renders periodic digest pushes.
//
// Each due subscription is pushed in its own panic-recovered goroutine
// with a hard timeout and an in-flight guard, so a slow rm-search or LLM
// upstream can never block the scheduling loop or other subscriptions.
package push

import (
	"context"
	"runtime/debug"
	"sync"
	"time"

	"github.com/sirupsen/logrus"

	"github.com/unnc-aim/aim-feishu-rm-assistant/internal/bot"
	"github.com/unnc-aim/aim-feishu-rm-assistant/internal/store"
)

// Push tuning: one digest may take a couple of LLM round trips.
const (
	pushCheckInterval = 30 * time.Second
	pushTimeout       = 5 * time.Minute
)

// Scheduler periodically checks subscriptions and sends digests.
type Scheduler struct {
	Bot   *bot.Bot
	Store *store.Store

	inFlight sync.Map // chatID -> struct{}{}
}

// Start runs the scheduling loop until ctx is cancelled.
func (s *Scheduler) Start(ctx context.Context) {
	ticker := time.NewTicker(pushCheckInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			logrus.Info("push scheduler stopped")
			return
		case <-ticker.C:
			s.tick(ctx)
		}
	}
}

// tick evaluates all active subscriptions once.
func (s *Scheduler) tick(ctx context.Context) {
	defer func() {
		if r := recover(); r != nil {
			logrus.Errorf("panic in push scheduler tick: %v\n%s", r, debug.Stack())
		}
	}()

	subs, err := s.Store.ActiveSubscriptions()
	if err != nil {
		logrus.Errorf("load subscriptions: %v", err)
		return
	}

	now := time.Now()
	due := 0
	for _, sub := range subs {
		if !s.due(sub, now) {
			continue
		}
		due++
		if _, busy := s.inFlight.LoadOrStore(sub.ChatID, struct{}{}); busy {
			logrus.WithField("chat_id", sub.ChatID).
				Warn("push already in flight, skipping this slot")
			continue
		}
		go s.pushOneGuarded(sub, now)
	}
	if due > 0 {
		logrus.Infof("push tick: %d subscriptions due", due)
	}
}

// pushOneGuarded wraps one push with panic recovery, a timeout and the
// in-flight release.
func (s *Scheduler) pushOneGuarded(sub *store.Settings, now time.Time) {
	defer s.inFlight.Delete(sub.ChatID)
	defer func() {
		if r := recover(); r != nil {
			logrus.WithField("chat_id", sub.ChatID).
				Errorf("panic in digest push: %v\n%s", r, debug.Stack())
		}
	}()

	ctx, cancel := context.WithTimeout(context.Background(), pushTimeout)
	defer cancel()

	start := time.Now()
	if err := s.pushOne(ctx, sub, now); err != nil {
		logrus.WithField("chat_id", sub.ChatID).
			Errorf("digest push failed after %s: %v",
				time.Since(start).Round(time.Millisecond), err)
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
func (s *Scheduler) pushOne(ctx context.Context, sub *store.Settings, now time.Time) error {
	start, end := windowOf(sub, now)
	if done, err := s.Store.WasPushed(sub.ChatID, start, end); err != nil {
		return err
	} else if done {
		// Refill last_push_at so due() stops firing for this slot.
		logrus.WithField("chat_id", sub.ChatID).Infof(
			"period %s ~ %s already pushed, refilling last_push_at", start, end)
		return s.markOnly(sub.ChatID)
	}

	logrus.WithFields(logrus.Fields{
		"chat_id": sub.ChatID,
		"window":  start.Format("01-02 15:04") + " ~ " + end.Format("01-02 15:04"),
	}).Infof("building %s digest", sub.Frequency)

	card, err := s.BuildDigest(ctx, start, end)
	if err != nil {
		return err
	}
	if err := s.Bot.SendCard(ctx, sub.ChatID, card); err != nil {
		return err
	}
	if err := s.Store.MarkPushed(sub.ChatID, start, end); err != nil {
		return err
	}
	logrus.WithFields(logrus.Fields{
		"chat_id":  sub.ChatID,
		"duration": time.Since(now).Round(time.Millisecond).String(),
	}).Infof("%s digest pushed (%s ~ %s)", sub.Frequency, start, end)
	return nil
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
