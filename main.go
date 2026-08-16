// aim-feishu-rm-assistant is a Feishu bot that searches the rm-search
// production API and pushes periodic RoboMaster digests to subscribers.
package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"
	"time"

	lark "github.com/larksuite/oapi-sdk-go/v3"
	"github.com/sirupsen/logrus"

	"github.com/unnc-aim/aim-feishu-rm-assistant/internal/bot"
	"github.com/unnc-aim/aim-feishu-rm-assistant/internal/config"
	"github.com/unnc-aim/aim-feishu-rm-assistant/internal/llm"
	"github.com/unnc-aim/aim-feishu-rm-assistant/internal/log"
	"github.com/unnc-aim/aim-feishu-rm-assistant/internal/push"
	"github.com/unnc-aim/aim-feishu-rm-assistant/internal/rmsearch"
	"github.com/unnc-aim/aim-feishu-rm-assistant/internal/store"
	"github.com/unnc-aim/aim-feishu-rm-assistant/internal/tz"
)

func main() {
	tz.ResolveLocal()

	cfg := config.FromEnv()
	logCloser, err := log.Setup(cfg.LogDir)
	if err != nil {
		logrus.Fatalf("setup file logging: %v", err)
	}
	defer logCloser.Close()

	if err := cfg.Validate(); err != nil {
		logrus.Fatal(err)
	}
	if !llm.NewClient(cfg.LLMBaseURL, cfg.LLMAPIKey, cfg.LLMModel).Enabled() {
		logrus.Warn("LLM_API_KEY is empty, summaries and digests fall back to plain lists")
	}

	st, err := store.Open(cfg.SQLitePath)
	if err != nil {
		logrus.Fatalf("open store: %v", err)
	}
	defer st.Close()

	logrus.Infof("logging to daily files in %s", cfg.LogDir)

	larkClient := lark.NewClient(cfg.AppID, cfg.AppSecret,
		lark.WithReqTimeout(15*time.Second))
	searchClient := rmsearch.NewClient(cfg.RmSearchBaseURL)
	llmClient := llm.NewClient(cfg.LLMBaseURL, cfg.LLMAPIKey, cfg.LLMModel)
	b := bot.New(larkClient, cfg.AppID, cfg.AppSecret, searchClient, llmClient, st, cfg.PushDefaultHour, cfg.PushDefaultMinute)

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	go func() {
		s := &push.Scheduler{Bot: b, Store: st}
		b.ManualDigest = func(ctx context.Context, chatID string) error {
			end := time.Now()
			start := end.Add(-24 * time.Hour)
			card, err := s.BuildDigest(ctx, start, end, "本周动态", push.WeekStart(end), end)
			if err != nil {
				return err
			}
			return b.SendCard(ctx, chatID, card)
		}
		s.Start(ctx)
	}()

	wsClient := b.NewWSClient()

	logrus.Info("starting feishu rm assistant")

	// The SDK's Start ends with a bare select{} and never returns on ctx
	// cancellation, so it must run in its own goroutine; shutdown is driven
	// by the signal context below instead of Start returning.
	startErr := make(chan error, 1)
	go func() {
		startErr <- wsClient.Start(ctx)
	}()

	select {
	case err := <-startErr:
		// Start only returns on connect failure; a nil return should not
		// happen but is treated as a fatal exit either way.
		logrus.Errorf("ws client exited: %v", err)
		os.Exit(1)
	case <-ctx.Done():
		logrus.Info("shutdown signal received, closing websocket")
	}

	wsClient.Close()

	// Hard-exit watchdog: if any deferred cleanup hangs, the process still
	// exits well within the docker stop grace period instead of being
	// SIGKILLed after 10s.
	time.AfterFunc(5*time.Second, func() {
		logrus.Warn("graceful shutdown timed out, forcing exit")
		os.Exit(0)
	})

	logrus.Info("shutdown complete")
}
