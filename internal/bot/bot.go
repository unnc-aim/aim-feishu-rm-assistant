// Package bot implements the Feishu bot: receiving messages over the
// WebSocket long connection and replying with interactive cards.
//
// The SDK dispatches event frames serially on its WebSocket read loop, so
// the receive callback must never block on upstream calls. Incoming
// messages are therefore validated and logged in the callback, then handed
// to a bounded worker pool (hash-routed per chat to preserve ordering).
// Every downstream call has a timeout, every goroutine has panic
// recovery, and every step is logged.
//
// In group chats the bot only reacts when mentioned by its own identity
// (bot/v3/info open_id), and group replies mention the asking user.
//
// Note: card action callbacks are not wired because the official Go SDK
// stubs card frames on the long connection (ws.Client drops
// MessageTypeCard). All interaction therefore happens through chat text.
package bot

import (
	"context"
	"encoding/json"
	"fmt"
	"hash/fnv"
	"regexp"
	"runtime/debug"
	"strconv"
	"strings"
	"sync"
	"time"

	lark "github.com/larksuite/oapi-sdk-go/v3"
	larkcore "github.com/larksuite/oapi-sdk-go/v3/core"
	larkevents "github.com/larksuite/oapi-sdk-go/v3/event/dispatcher"
	larkim "github.com/larksuite/oapi-sdk-go/v3/service/im/v1"
	larkws "github.com/larksuite/oapi-sdk-go/v3/ws"
	"github.com/sirupsen/logrus"

	"github.com/unnc-aim/aim-feishu-rm-assistant/internal/llm"
	"github.com/unnc-aim/aim-feishu-rm-assistant/internal/rmsearch"
	"github.com/unnc-aim/aim-feishu-rm-assistant/internal/store"
)

// ResultsPerPage is the page size of search result cards.
const ResultsPerPage = 5

// Worker pool and timeout tuning. Messages of one chat always go to the
// same worker (hash routing) so replies stay ordered; different chats are
// processed in parallel. Bounded queues drop with a warning instead of
// ever blocking the WebSocket read loop.
const (
	workerCount    = 8
	queuePerWorker = 64
	handleTimeout  = 60 * time.Second
	sendTimeout    = 15 * time.Second

	// botInfoRetryInterval throttles retries when the bot/v3/info lookup
	// fails, so a group message storm cannot hammer the API.
	botInfoRetryInterval = time.Minute
)

// task is one queued user message; it doubles as the reply target.
type task struct {
	ChatID   string
	ChatType string
	SenderID string // asking user's open_id, mentioned in group replies
	Text     string
}

// searchState remembers the last query of a chat for pagination commands.
type searchState struct {
	Query  string
	Offset int
	At     time.Time
}

// Bot wires the Feishu SDK, the rm-search client, the LLM client and the
// store together.
type Bot struct {
	Lark      *lark.Client
	AppID     string
	AppSecret string
	Search    *rmsearch.Client
	LLM       *llm.Client
	Store     *store.Store
	Defaults  struct {
		PushHour   int
		PushMinute int
	}

	queues       []chan task
	lastSearch   map[string]*searchState
	lastSearchMu sync.Mutex

	botInfoMu      sync.Mutex
	botOpenID      string
	botInfoLastTry time.Time

	// ManualDigest, when wired, builds and sends a digest for the past 24h
	// on demand ("测试推送"), used to verify the push pipeline end to end.
	ManualDigest func(ctx context.Context, chatID string) error
}

// New creates a Bot and starts its worker pool.
func New(larkClient *lark.Client, appID, appSecret string, search *rmsearch.Client, llmClient *llm.Client, st *store.Store, defHour, defMinute int) *Bot {
	b := &Bot{
		Lark:       larkClient,
		AppID:      appID,
		AppSecret:  appSecret,
		Search:     search,
		LLM:        llmClient,
		Store:      st,
		queues:     make([]chan task, workerCount),
		lastSearch: map[string]*searchState{},
	}
	b.Defaults.PushHour = defHour
	b.Defaults.PushMinute = defMinute

	for i := range b.queues {
		b.queues[i] = make(chan task, queuePerWorker)
		go b.worker(i)
	}
	logrus.Infof("bot worker pool started: %d workers, %d slots each", workerCount, queuePerWorker)

	// Warm the bot identity cache so the first group mention works.
	go func() {
		defer recoverSilently("bot info warm-up")
		ctx, cancel := context.WithTimeout(context.Background(), sendTimeout)
		defer cancel()
		if _, err := b.BotSelfOpenID(ctx); err != nil {
			logrus.Warnf("bot info warm-up failed (will retry on demand): %v", err)
		}
	}()
	return b
}

// ---------- worker pool ----------

// worker drains its queue; one chat is always routed here.
func (b *Bot) worker(i int) {
	for t := range b.queues[i] {
		b.runTask(t)
	}
}

// runTask processes one message with a hard timeout and panic recovery so
// neither a slow upstream nor a bug can take down the process.
func (b *Bot) runTask(t task) {
	defer func() {
		if r := recover(); r != nil {
			logrus.WithFields(logrus.Fields{
				"chat_id": t.ChatID,
			}).Errorf("panic in message handler: %v\n%s", r, debug.Stack())
		}
	}()

	start := time.Now()
	ctx, cancel := context.WithTimeout(context.Background(), handleTimeout)
	defer cancel()

	if err := b.handleText(ctx, t); err != nil {
		logrus.WithField("chat_id", t.ChatID).
			Errorf("handle message %q failed after %s: %v",
				clip(t.Text, 100), time.Since(start).Round(time.Millisecond), err)
		return
	}
	logrus.WithFields(logrus.Fields{
		"chat_id":  t.ChatID,
		"duration": time.Since(start).Round(time.Millisecond).String(),
	}).Infof("handled message: %s", clip(t.Text, 100))
}

// enqueue routes a message to the worker owning its chat. It never
// blocks: on a full queue the message is dropped with a warning and a
// best-effort busy notice.
func (b *Bot) enqueue(t task) bool {
	idx := int(fnv32(t.ChatID) % uint32(workerCount))
	select {
	case b.queues[idx] <- t:
		return true
	default:
		logrus.WithFields(logrus.Fields{
			"chat_id": t.ChatID,
			"worker":  idx,
		}).Warnf("queue full, dropping message: %s", clip(t.Text, 100))
		go func() {
			defer recoverSilently("busy notice")
			ctx, cancel := context.WithTimeout(context.Background(), sendTimeout)
			defer cancel()
			_ = b.replyText(ctx, t, "消息处理繁忙, 请稍后重试。")
		}()
		return false
	}
}

func recoverSilently(what string) {
	if r := recover(); r != nil {
		logrus.Errorf("panic in %s: %v\n%s", what, r, debug.Stack())
	}
}

func fnv32(s string) uint32 {
	h := fnv.New32a()
	_, _ = h.Write([]byte(s))
	return h.Sum32()
}

// ---------- Feishu wiring ----------

// NewWSClient builds the WebSocket long-connection client.
func (b *Bot) NewWSClient() *larkws.Client {
	dispatcher := larkevents.NewEventDispatcher("", "").
		OnP2MessageReceiveV1(b.onMessageReceive)
	return larkws.NewClient(b.AppID, b.AppSecret,
		larkws.WithEventHandler(dispatcher),
		larkws.WithAutoReconnect(true),
	)
}

// onMessageReceive validates and logs the event, then hands the message to
// the worker pool. It always returns quickly so the SDK read loop is never
// blocked by upstream calls.
func (b *Bot) onMessageReceive(ctx context.Context, event *larkim.P2MessageReceiveV1) error {
	defer recoverSilently("onMessageReceive")

	msg := event.Event.Message
	if msg == nil || msg.MessageType == nil || *msg.MessageType != "text" {
		return nil
	}
	chatID := deref(msg.ChatId)
	if chatID == "" {
		return nil
	}
	chatType := deref(msg.ChatType)
	senderID := ""
	if event.Event.Sender != nil && event.Event.Sender.SenderId != nil {
		senderID = deref(event.Event.Sender.SenderId.OpenId)
	}

	text := extractText(msg.Content, msg.Mentions)

	// In a group chat the bot only reacts when mentioned by its own
	// identity; mentioning someone else must not trigger it.
	if chatType == "group" {
		selfID, err := b.BotSelfOpenID(ctx)
		if err != nil {
			logrus.WithField("chat_id", chatID).
				Warnf("cannot resolve bot identity, ignoring group message: %v", err)
			return nil
		}
		if !mentionsSelf(msg.Mentions, selfID) {
			return nil
		}
	}

	// Every user question/command is logged (to stderr and the log file).
	logrus.WithFields(logrus.Fields{
		"chat_id":   chatID,
		"chat_type": chatType,
		"sender":    senderID,
		"msg_type":  deref(msg.MessageType),
	}).Infof("received message: %s", clip(text, 200))

	b.enqueue(task{ChatID: chatID, ChatType: chatType, SenderID: senderID, Text: text})
	return nil
}

// BotSelfOpenID returns the bot's own open_id (bot/v3/info), cached after
// the first success. Failed lookups are retried at most once per interval.
func (b *Bot) BotSelfOpenID(ctx context.Context) (string, error) {
	b.botInfoMu.Lock()
	defer b.botInfoMu.Unlock()

	if b.botOpenID != "" {
		return b.botOpenID, nil
	}
	if !b.botInfoLastTry.IsZero() && time.Since(b.botInfoLastTry) < botInfoRetryInterval {
		next := (botInfoRetryInterval - time.Since(b.botInfoLastTry)).Round(time.Second)
		return "", fmt.Errorf("bot info lookup failed recently, next retry in %s", next)
	}
	b.botInfoLastTry = time.Now()

	resp, err := b.Lark.Get(ctx, "/open-apis/bot/v3/info", nil, larkcore.AccessTokenTypeTenant)
	if err != nil {
		return "", fmt.Errorf("get bot info: %w", err)
	}
	if resp.StatusCode != 200 {
		return "", fmt.Errorf("get bot info: status %d", resp.StatusCode)
	}
	var payload struct {
		Code int    `json:"code"`
		Msg  string `json:"msg"`
		Bot  struct {
			OpenID  string `json:"open_id"`
			AppName string `json:"app_name"`
		} `json:"bot"`
	}
	if err := json.Unmarshal(resp.RawBody, &payload); err != nil {
		return "", fmt.Errorf("decode bot info: %w", err)
	}
	if payload.Code != 0 || payload.Bot.OpenID == "" {
		return "", fmt.Errorf("get bot info: code %d, msg %s", payload.Code, payload.Msg)
	}
	b.botOpenID = payload.Bot.OpenID
	logrus.Infof("bot identity resolved: open_id=%s name=%s", payload.Bot.OpenID, payload.Bot.AppName)
	return b.botOpenID, nil
}

// handleText routes one validated message to help / pagination / intents /
// search.
func (b *Bot) handleText(ctx context.Context, t task) error {
	if strings.TrimSpace(t.Text) == "" {
		return b.replyText(ctx, t, "请发送关键词开始搜索，或回复“帮助”查看用法。")
	}

	if isHelp(t.Text) {
		return b.replyCard(ctx, t, HelpCard())
	}

	// Manual digest trigger verifies the push pipeline end to end.
	if t.Text == "测试推送" || t.Text == "立即推送" {
		return b.manualDigest(ctx, t)
	}

	// Pagination commands act on the previous query of this chat.
	if page, ok := parsePageCommand(t.Text); ok {
		return b.doPage(ctx, t, page)
	}

	// Subscription and settings intents take precedence over search.
	if intent := ParseIntent(t.Text); intent.Kind != IntentNone {
		return b.handleIntent(ctx, t, intent)
	}

	return b.doSearch(ctx, t, t.Text, 0)
}

// ---------- search ----------

// doSearch queries rm-search, replies with a result card, then optionally
// follows up with an LLM summary message.
func (b *Bot) doSearch(ctx context.Context, t task, query string, offset int) error {
	start := time.Now()
	res, err := b.Search.Search(ctx, &rmsearch.SearchRequest{
		Q:      query,
		Limit:  ResultsPerPage,
		Offset: offset,
	})
	if err != nil {
		logrus.WithField("chat_id", t.ChatID).
			Warnf("search %q (offset %d) failed after %s: %v",
				query, offset, time.Since(start).Round(time.Millisecond), err)
		return b.replyText(ctx, t, "搜索失败, 请稍后重试。")
	}
	logrus.WithFields(logrus.Fields{
		"chat_id":  t.ChatID,
		"duration": time.Since(start).Round(time.Millisecond).String(),
		"hits":     len(res.Hits),
		"total":    res.Total,
	}).Infof("search %q (offset %d)", query, offset)

	settings, err := b.Store.GetSettings(t.ChatID, "p2p")
	if err != nil {
		logrus.WithField("chat_id", t.ChatID).Warnf("load settings failed, using defaults: %v", err)
		settings = &store.Settings{SummaryOn: true}
	}

	b.rememberSearch(t.ChatID, query, offset)

	if len(res.Hits) == 0 {
		if offset > 0 {
			return b.replyCard(ctx, t, EmptyResultCard("没有更多结果了"))
		}
		return b.replyCard(ctx, t, EmptyResultCard(query))
	}

	if err := b.replyCard(ctx, t, SearchResultCard(query, res, offset, ResultsPerPage, settings.SummaryOn)); err != nil {
		return fmt.Errorf("send result card: %w", err)
	}

	// Async LLM summary follow-up, disabled per-chat when summary_on = 0.
	// Only the first page triggers a summary to keep cost bounded. On
	// failure it retries with exponential backoff and, if still failing,
	// only logs — the user is never shown an error message.
	if offset == 0 && settings.SummaryOn && b.LLM.Enabled() {
		go func() {
			defer recoverSilently("search summary")
			b.sendSummaryWithRetry(t, query, res.Hits)
		}()
	}
	return nil
}

// Summary retry tuning: 10 retries, backoff starting at 5s, doubling,
// capped at 2 minutes (5,10,20,40,80,120,120,120,120,120s).
const (
	summaryMaxRetries  = 10
	summaryBackoffBase = 5 * time.Second
	summaryBackoffMax  = 2 * time.Minute
	summaryCallTimeout = 90 * time.Second
)

// summaryRetryDelay returns the wait before retry i (0-based).
func summaryRetryDelay(i int) time.Duration {
	d := summaryBackoffBase << i
	if d > summaryBackoffMax || d <= 0 { // <=0 guards shift overflow
		return summaryBackoffMax
	}
	return d
}

// sendSummaryWithRetry summarizes a search and sends the summary card,
// retrying on failure with exponential backoff. After exhausting the
// retries it logs the error and gives up silently.
func (b *Bot) sendSummaryWithRetry(t task, query string, hits []rmsearch.Document) {
	var (
		text    string
		lastErr error
	)
	for attempt := 0; attempt <= summaryMaxRetries; attempt++ {
		if attempt > 0 {
			time.Sleep(summaryRetryDelay(attempt - 1))
		}
		sctx, cancel := context.WithTimeout(context.Background(), summaryCallTimeout)
		start := time.Now()
		text, lastErr = SummarizeSearch(sctx, b.LLM, query, hits)
		cancel()
		if lastErr == nil {
			logrus.WithFields(logrus.Fields{
				"chat_id":  t.ChatID,
				"duration": time.Since(start).Round(time.Millisecond).String(),
				"attempt":  attempt + 1,
			}).Infof("search summary ready for %q", query)
			break
		}
		logrus.WithField("chat_id", t.ChatID).Warnf(
			"search summary for %q attempt %d/%d failed after %s: %v",
			query, attempt+1, summaryMaxRetries+1,
			time.Since(start).Round(time.Millisecond), lastErr)
	}
	if lastErr != nil {
		logrus.WithField("chat_id", t.ChatID).Errorf(
			"search summary for %q gave up after %d attempts: %v",
			query, summaryMaxRetries+1, lastErr)
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), sendTimeout)
	defer cancel()
	if err := b.replyCard(ctx, t, SummaryCard(query, text)); err != nil {
		logrus.WithField("chat_id", t.ChatID).Warnf("send summary card failed: %v", err)
	}
}

// pushPipelineTimeout bounds a manual digest build, matching the scheduler
// push timeout.
const pushPipelineTimeout = 5 * time.Minute

// manualDigest triggers an immediate digest build for the past 24h so the
// push pipeline can be verified without waiting for the schedule.
func (b *Bot) manualDigest(ctx context.Context, t task) error {
	if b.ManualDigest == nil {
		return b.replyText(ctx, t, "推送组件未就绪。")
	}
	if err := b.replyText(ctx, t, "正在生成最近 24 小时的推送, 请稍候..."); err != nil {
		return err
	}
	go func() {
		defer recoverSilently("manual digest")
		start := time.Now()
		mctx, cancel := context.WithTimeout(context.Background(), pushPipelineTimeout)
		defer cancel()
		if err := b.ManualDigest(mctx, t.ChatID); err != nil {
			logrus.WithField("chat_id", t.ChatID).Errorf("manual digest failed after %s: %v",
				time.Since(start).Round(time.Millisecond), err)
			sctx, scancel := context.WithTimeout(context.Background(), sendTimeout)
			defer scancel()
			_ = b.replyText(sctx, t, "推送生成失败: "+clip(err.Error(), 120))
			return
		}
		logrus.WithFields(logrus.Fields{
			"chat_id":  t.ChatID,
			"duration": time.Since(start).Round(time.Millisecond).String(),
		}).Info("manual digest sent")
	}()
	return nil
}

// doPage handles "下一页" / "上一页" / "第N页" style commands.
func (b *Bot) doPage(ctx context.Context, t task, pageDeltaOrIndex pageRef) error {
	state := b.recallSearch(t.ChatID)
	if state == nil {
		return b.replyText(ctx, t, "请先发送一个搜索关键词，再翻页。")
	}
	offset := 0
	if pageDeltaOrIndex.absolute {
		offset = (pageDeltaOrIndex.page - 1) * ResultsPerPage
	} else {
		offset = state.Offset + pageDeltaOrIndex.page*ResultsPerPage
	}
	if offset < 0 {
		offset = 0
	}
	return b.doSearch(ctx, t, state.Query, offset)
}

// rememberSearch stores the last query state of a chat.
func (b *Bot) rememberSearch(chatID, query string, offset int) {
	b.lastSearchMu.Lock()
	defer b.lastSearchMu.Unlock()
	b.lastSearch[chatID] = &searchState{Query: query, Offset: offset, At: time.Now()}
}

// recallSearch returns the last query state of a chat, if recent enough.
func (b *Bot) recallSearch(chatID string) *searchState {
	b.lastSearchMu.Lock()
	defer b.lastSearchMu.Unlock()
	s := b.lastSearch[chatID]
	if s == nil || time.Since(s.At) > 30*time.Minute {
		return nil
	}
	return s
}

// ---------- settings ----------

// handleIntent applies a subscription/settings intent and confirms with the
// settings card.
func (b *Bot) handleIntent(ctx context.Context, t task, intent *Intent) error {
	logrus.WithField("chat_id", t.ChatID).Infof("intent kind=%d hasTime=%v time=%02d:%02d",
		intent.Kind, intent.HasTime, intent.Hour, intent.Minute)

	// Make sure the chat has a settings row.
	if _, err := b.Store.GetSettings(t.ChatID, t.ChatType); err != nil {
		return fmt.Errorf("ensure settings: %w", err)
	}

	hour, minute := b.Defaults.PushHour, b.Defaults.PushMinute
	if intent.HasTime {
		hour, minute = intent.Hour, intent.Minute
	}

	switch intent.Kind {
	case IntentUnsubscribe:
		if err := b.Store.Unsubscribe(t.ChatID); err != nil {
			return fmt.Errorf("unsubscribe: %w", err)
		}
	case IntentSummaryOn:
		if err := b.Store.SetSummary(t.ChatID, true); err != nil {
			return fmt.Errorf("set summary on: %w", err)
		}
	case IntentSummaryOff:
		if err := b.Store.SetSummary(t.ChatID, false); err != nil {
			return fmt.Errorf("set summary off: %w", err)
		}
	case IntentSubscribeDaily:
		if err := b.Store.UpsertSubscription(t.ChatID, store.FrequencyDaily, hour, minute); err != nil {
			return fmt.Errorf("subscribe daily: %w", err)
		}
	case IntentSubscribeWeekly:
		if err := b.Store.UpsertSubscription(t.ChatID, store.FrequencyWeekly, hour, minute); err != nil {
			return fmt.Errorf("subscribe weekly: %w", err)
		}
	default:
		return nil
	}
	return b.replyCard(ctx, t, SettingsCard(b.settingsView(t.ChatID)))
}

func (b *Bot) settingsView(chatID string) *store.Settings {
	st, err := b.Store.GetSettings(chatID, "p2p")
	if err != nil {
		logrus.WithField("chat_id", chatID).Warnf("load settings failed: %v", err)
		return &store.Settings{ChatID: chatID, SummaryOn: true}
	}
	return st
}

// ---------- sending ----------

// atMarkup returns the lark_md mention of the asking user, empty in p2p
// chats where an at would be noise.
func (t task) atMarkup() string {
	if t.ChatType == "group" && t.SenderID != "" {
		return fmt.Sprintf(`<at user_id="%s"></at>`, t.SenderID)
	}
	return ""
}

// replyText sends a plain text message, mentioning the asker in groups.
func (b *Bot) replyText(ctx context.Context, t task, text string) error {
	if at := t.atMarkup(); at != "" {
		text = at + " " + text
	}
	return b.SendText(ctx, t.ChatID, text)
}

// replyCard sends an interactive card, mentioning the asker in groups by
// prepending an at element.
func (b *Bot) replyCard(ctx context.Context, t task, card map[string]any) error {
	if at := t.atMarkup(); at != "" {
		prependAtElement(card, at)
	}
	return b.SendCard(ctx, t.ChatID, card)
}

// SendText sends a plain text message to a chat.
func (b *Bot) SendText(ctx context.Context, chatID, text string) error {
	content, err := json.Marshal(map[string]string{"text": text})
	if err != nil {
		return err
	}
	req := larkim.NewCreateMessageReqBuilder().
		ReceiveIdType("chat_id").
		Body(larkim.NewCreateMessageReqBodyBuilder().
			ReceiveId(chatID).
			MsgType("text").
			Content(string(content)).
			Build()).
		Build()
	_, err = b.Lark.Im.V1.Message.Create(ctx, req)
	if err != nil {
		logrus.WithField("chat_id", chatID).Warnf("send text failed: %v", err)
	}
	return err
}

// SendCard sends an interactive card to a chat.
func (b *Bot) SendCard(ctx context.Context, chatID string, card map[string]any) error {
	content, err := json.Marshal(card)
	if err != nil {
		return err
	}
	req := larkim.NewCreateMessageReqBuilder().
		ReceiveIdType("chat_id").
		Body(larkim.NewCreateMessageReqBodyBuilder().
			ReceiveId(chatID).
			MsgType("interactive").
			Content(string(content)).
			Build()).
		Build()
	_, err = b.Lark.Im.V1.Message.Create(ctx, req)
	if err != nil {
		logrus.WithField("chat_id", chatID).Warnf("send card failed: %v", err)
	}
	return err
}

// ---------- message parsing ----------

// pageRef is either a relative page turn or an absolute page number.
type pageRef struct {
	page     int
	absolute bool
}

var (
	pageAbsPattern = regexp.MustCompile(`^第\s*(\d{1,3})\s*页$`)
	nextPageWords  = []string{"下一页", "下页", "更多", "more", "继续"}
	prevPageWords  = []string{"上一页", "上页", "prev"}
)

// parsePageCommand recognizes pagination commands.
func parsePageCommand(text string) (pageRef, bool) {
	t := strings.TrimSpace(text)
	if m := pageAbsPattern.FindStringSubmatch(t); m != nil {
		n, err := strconv.Atoi(m[1])
		if err == nil && n >= 1 && n <= 200 {
			return pageRef{page: n, absolute: true}, true
		}
		return pageRef{}, false
	}
	for _, w := range nextPageWords {
		if t == w || strings.HasPrefix(t, w+" ") {
			return pageRef{page: 1}, true
		}
	}
	for _, w := range prevPageWords {
		if t == w || strings.HasPrefix(t, w+" ") {
			return pageRef{page: -1}, true
		}
	}
	return pageRef{}, false
}

// extractText decodes the message content JSON and strips mention keys such
// as @_user_1 so the remaining text is the user query.
func extractText(content *string, mentions []*larkim.MentionEvent) string {
	raw := deref(content)
	if raw == "" {
		return ""
	}
	var payload struct {
		Text string `json:"text"`
	}
	if err := json.Unmarshal([]byte(raw), &payload); err != nil {
		return ""
	}
	text := payload.Text
	for _, m := range mentions {
		if m != nil && m.Key != nil {
			text = strings.ReplaceAll(text, *m.Key, "")
		}
	}
	return strings.TrimSpace(text)
}

// mentionsSelf reports whether the mention list contains the given open_id.
func mentionsSelf(mentions []*larkim.MentionEvent, openID string) bool {
	for _, m := range mentions {
		if m != nil && m.Id != nil && deref(m.Id.OpenId) == openID {
			return true
		}
	}
	return false
}

func isHelp(text string) bool {
	t := strings.TrimSpace(text)
	return t == "帮助" || t == "help" || t == "Help" || t == "/help" || t == "菜单"
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}
