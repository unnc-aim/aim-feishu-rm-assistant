// aim-feishu-rm-assistant is a Feishu bot that searches the rm-search
// production API and pushes periodic RoboMaster digests to subscribers.
package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"

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
	logCloser, err := log.Setup(cfg.LogPath)
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

	logrus.Infof("logging to file %s", cfg.LogPath)

	larkClient := lark.NewClient(cfg.AppID, cfg.AppSecret)
	searchClient := rmsearch.NewClient(cfg.RmSearchBaseURL)
	llmClient := llm.NewClient(cfg.LLMBaseURL, cfg.LLMAPIKey, cfg.LLMModel)
	b := bot.New(larkClient, cfg.AppID, cfg.AppSecret, searchClient, llmClient, st, cfg.PushDefaultHour, cfg.PushDefaultMinute)

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	go func() {
		s := &push.Scheduler{Bot: b, Store: st}
		s.Start(ctx)
	}()

	wsClient := b.NewWSClient()

	logrus.Info("starting feishu rm assistant")
	if err := wsClient.Start(ctx); err != nil {
		logrus.Errorf("ws client exited: %v", err)
		os.Exit(1)
	}
}
