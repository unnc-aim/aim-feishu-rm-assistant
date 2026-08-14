// Package bot implements the Feishu bot: receiving messages over the
// WebSocket long connection and replying with interactive cards.
//
// Note: card action callbacks are not wired because the official Go SDK
// stubs card frames on the long connection (ws.Client drops
// MessageTypeCard). All interaction therefore happens through chat text.
package bot

import (
	"context"
	"encoding/json"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	lark "github.com/larksuite/oapi-sdk-go/v3"
	larkevents "github.com/larksuite/oapi-sdk-go/v3/event/dispatcher"
	larkim "github.com/larksuite/oapi-sdk-go/v3/service/im/v1"
	larkws "github.com/larksuite/oapi-sdk-go/v3/ws"
	"github.com/sirupsen/logrus"

	"github.com/UNNC-AIM/aim-feishu-rm-assistant/internal/llm"
	"github.com/UNNC-AIM/aim-feishu-rm-assistant/internal/rmsearch"
	"github.com/UNNC-AIM/aim-feishu-rm-assistant/internal/store"
)

// ResultsPerPage is the page size of search result cards.
const ResultsPerPage = 5

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

	lastSearch   map[string]*searchState
	lastSearchMu sync.Mutex
}

// New creates a Bot.
func New(larkClient *lark.Client, appID, appSecret string, search *rmsearch.Client, llmClient *llm.Client, st *store.Store, defHour, defMinute int) *Bot {
	b := &Bot{
		Lark:       larkClient,
		AppID:      appID,
		AppSecret:  appSecret,
		Search:     search,
		LLM:        llmClient,
		Store:      st,
		lastSearch: map[string]*searchState{},
	}
	b.Defaults.PushHour = defHour
	b.Defaults.PushMinute = defMinute
	return b
}

// NewWSClient builds the WebSocket long-connection client.
func (b *Bot) NewWSClient() *larkws.Client {
	dispatcher := larkevents.NewEventDispatcher("", "").
		OnP2MessageReceiveV1(b.onMessageReceive)
	return larkws.NewClient(b.AppID, b.AppSecret,
		larkws.WithEventHandler(dispatcher),
		larkws.WithAutoReconnect(true),
	)
}

// onMessageReceive handles a text message event.
func (b *Bot) onMessageReceive(ctx context.Context, event *larkim.P2MessageReceiveV1) error {
	msg := event.Event.Message
	if msg == nil || msg.MessageType == nil || *msg.MessageType != "text" {
		return nil
	}
	chatID := deref(msg.ChatId)
	if chatID == "" {
		return nil
	}
	chatType := deref(msg.ChatType)

	text := extractText(msg.Content, msg.Mentions)

	// In a group chat the bot only reacts when mentioned; in a p2p chat
	// every text message is treated as input.
	if chatType == "group" && !mentionsBot(msg.Mentions) {
		return nil
	}

	if strings.TrimSpace(text) == "" {
		return b.SendText(ctx, chatID, "请发送关键词开始搜索，或回复“帮助”查看用法。")
	}

	if isHelp(text) {
		return b.SendCard(ctx, chatID, HelpCard())
	}

	// Pagination commands act on the previous query of this chat.
	if page, ok := parsePageCommand(text); ok {
		return b.doPage(ctx, chatID, page)
	}

	// Subscription and settings intents take precedence over search.
	if intent := ParseIntent(text); intent.Kind != IntentNone {
		return b.handleIntent(ctx, chatID, chatType, intent)
	}

	return b.doSearch(ctx, chatID, text, 0)
}

// doSearch queries rm-search, replies with a result card, then optionally
// follows up with an LLM summary message.
func (b *Bot) doSearch(ctx context.Context, chatID, query string, offset int) error {
	res, err := b.Search.Search(ctx, &rmsearch.SearchRequest{
		Q:      query,
		Limit:  ResultsPerPage,
		Offset: offset,
	})
	if err != nil {
		return b.SendText(ctx, chatID, "搜索失败: "+err.Error())
	}

	settings, err := b.Store.GetSettings(chatID, "p2p")
	if err != nil {
		settings = &store.Settings{SummaryOn: true}
	}

	b.rememberSearch(chatID, query, offset)

	if len(res.Hits) == 0 {
		if offset > 0 {
			return b.SendCard(ctx, chatID, EmptyResultCard("没有更多结果了"))
		}
		return b.SendCard(ctx, chatID, EmptyResultCard(query))
	}

	if err := b.SendCard(ctx, chatID, SearchResultCard(query, res, offset, ResultsPerPage, settings.SummaryOn)); err != nil {
		return err
	}

	// Async LLM summary follow-up, disabled per-chat when summary_on = 0.
	// Only the first page triggers a summary to keep cost bounded. On
	// failure it retries with exponential backoff and, if still failing,
	// only logs — the user is never shown an error message.
	if offset == 0 && settings.SummaryOn && b.LLM.Enabled() {
		go b.sendSummaryWithRetry(chatID, query, res.Hits)
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
func (b *Bot) sendSummaryWithRetry(chatID, query string, hits []rmsearch.Document) {
	var (
		text    string
		lastErr error
	)
	for attempt := 0; attempt <= summaryMaxRetries; attempt++ {
		if attempt > 0 {
			time.Sleep(summaryRetryDelay(attempt - 1))
		}
		sctx, cancel := context.WithTimeout(context.Background(), summaryCallTimeout)
		text, lastErr = SummarizeSearch(sctx, b.LLM, query, hits)
		cancel()
		if lastErr == nil {
			break
		}
		logrus.Warnf("search summary for %q attempt %d/%d failed: %v",
			query, attempt+1, summaryMaxRetries+1, lastErr)
	}
	if lastErr != nil {
		logrus.Errorf("search summary for %q gave up after %d attempts: %v",
			query, summaryMaxRetries+1, lastErr)
		return
	}
	_ = b.SendCard(context.Background(), chatID, SummaryCard(query, text))
}

// doPage handles "下一页" / "上一页" / "第N页" style commands.
func (b *Bot) doPage(ctx context.Context, chatID string, pageDeltaOrIndex pageRef) error {
	state := b.recallSearch(chatID)
	if state == nil {
		return b.SendText(ctx, chatID, "请先发送一个搜索关键词，再翻页。")
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
	return b.doSearch(ctx, chatID, state.Query, offset)
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

// handleIntent applies a subscription/settings intent and confirms with the
// settings card.
func (b *Bot) handleIntent(ctx context.Context, chatID, chatType string, intent *Intent) error {
	// Make sure the chat has a settings row.
	if _, err := b.Store.GetSettings(chatID, chatType); err != nil {
		return err
	}

	hour, minute := b.Defaults.PushHour, b.Defaults.PushMinute
	if intent.HasTime {
		hour, minute = intent.Hour, intent.Minute
	}

	switch intent.Kind {
	case IntentUnsubscribe:
		if err := b.Store.Unsubscribe(chatID); err != nil {
			return err
		}
	case IntentSummaryOn:
		if err := b.Store.SetSummary(chatID, true); err != nil {
			return err
		}
	case IntentSummaryOff:
		if err := b.Store.SetSummary(chatID, false); err != nil {
			return err
		}
	case IntentSubscribeDaily:
		if err := b.Store.UpsertSubscription(chatID, store.FrequencyDaily, hour, minute); err != nil {
			return err
		}
	case IntentSubscribeWeekly:
		if err := b.Store.UpsertSubscription(chatID, store.FrequencyWeekly, hour, minute); err != nil {
			return err
		}
	default:
		return nil
	}
	return b.SendCard(ctx, chatID, SettingsCard(b.settingsView(chatID)))
}

func (b *Bot) settingsView(chatID string) *store.Settings {
	st, err := b.Store.GetSettings(chatID, "p2p")
	if err != nil {
		return &store.Settings{ChatID: chatID, SummaryOn: true}
	}
	return st
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
	return err
}

// SendCard sends an interactive card to a chat.
func (b *Bot) SendCard(ctx context.Context, chatID string, card map[string]interface{}) error {
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
	return err
}

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

func mentionsBot(mentions []*larkim.MentionEvent) bool {
	for _, m := range mentions {
		if m != nil && m.Id != nil && m.Id.OpenId != nil {
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
