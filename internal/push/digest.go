package push

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/sirupsen/logrus"

	"github.com/unnc-aim/aim-feishu-rm-assistant/internal/bot"
	"github.com/unnc-aim/aim-feishu-rm-assistant/internal/llm"
	"github.com/unnc-aim/aim-feishu-rm-assistant/internal/rmsearch"
)

// Digest tuning constants.
const (
	announceCandidateLimit = 50
	forumCandidateLimit    = 100
	pickLimit              = 10
)

// selection is the LLM's pick of one candidate item.
type selection struct {
	Index  int    `json:"index"`
	Reason string `json:"reason"`
}

// windowData bundles the fetched content of one digest window.
type windowData struct {
	Start     time.Time
	End       time.Time
	Announces []rmsearch.Document
	Forums    []rmsearch.Document
}

// BuildDigest assembles the digest card for the primary window [start,
// end), optionally followed by a secondary window block (e.g. this week
// for daily pushes, this month for weekly pushes). Empty windows are
// rendered as reminders, never skipped.
func (s *Scheduler) BuildDigest(ctx context.Context, start, end time.Time, secLabel string, secStart, secEnd time.Time) (map[string]interface{}, error) {
	primary, err := s.fetchWindow(ctx, start, end)
	if err != nil {
		return nil, err
	}

	var secondary *windowData
	if secLabel != "" && !secStart.IsZero() && secEnd.After(secStart) {
		if secondary, err = s.fetchWindow(ctx, secStart, secEnd); err != nil {
			return nil, fmt.Errorf("fetch secondary window: %w", err)
		}
	}

	return s.renderDigest(ctx, primary, secLabel, secondary), nil
}

// fetchWindow pulls announcements and forum posts created in [start, end).
func (s *Scheduler) fetchWindow(ctx context.Context, start, end time.Time) (*windowData, error) {
	startMS := start.UnixMilli()
	endMS := end.UnixMilli()

	t0 := time.Now()
	announces, err := s.Bot.Search.LatestSince(ctx, []string{rmsearch.SourceAnnounce}, startMS, announceCandidateLimit)
	if err != nil {
		return nil, fmt.Errorf("fetch announces: %w", err)
	}
	logrus.WithFields(logrus.Fields{
		"duration": time.Since(t0).Round(time.Millisecond).String(),
		"count":    len(announces),
	}).Info("digest stage: fetched announces")

	t1 := time.Now()
	forums, err := s.Bot.Search.LatestSince(ctx,
		[]string{rmsearch.SourceArticle, rmsearch.SourceFAQ, rmsearch.SourceWiki}, startMS, forumCandidateLimit)
	if err != nil {
		return nil, fmt.Errorf("fetch forum posts: %w", err)
	}
	logrus.WithFields(logrus.Fields{
		"duration": time.Since(t1).Round(time.Millisecond).String(),
		"count":    len(forums),
	}).Info("digest stage: fetched forum posts")

	// Filter the upper bound client-side: the API filter only supports
	// create_time > start, and late-indexed older items may leak in.
	announces = before(endMS, announces)
	forums = before(endMS, forums)

	return &windowData{Start: start, End: end, Announces: announces, Forums: forums}, nil
}

func before(endMS int64, docs []rmsearch.Document) []rmsearch.Document {
	out := docs[:0]
	for _, d := range docs {
		if d.CreateTimeMS > 0 && d.CreateTimeMS < endMS {
			out = append(out, d)
		}
	}
	return out
}

func (s *Scheduler) renderDigest(ctx context.Context, primary *windowData, secLabel string, secondary *windowData) map[string]interface{} {
	title := "RM 日报"
	if primary.End.Sub(primary.Start) > 48*time.Hour {
		title = "RM 周报"
	}

	elements := []map[string]interface{}{
		mdElement(fmt.Sprintf("%s ~ %s",
			primary.Start.In(time.Local).Format("01-02 15:04"), primary.End.In(time.Local).Format("01-02 15:04"))),
	}
	elements = append(elements, s.windowElements(ctx, primary)...)

	if secondary != nil {
		elements = append(elements, divider(),
			mdElement(fmt.Sprintf("**%s** (%s ~ %s)", secLabel,
				secondary.Start.In(time.Local).Format("01-02"), secondary.End.In(time.Local).Format("01-02 15:04"))))
		elements = append(elements, s.windowElements(ctx, secondary)...)
	}

	elements = append(elements, noteEl("数据来源: RM Search · 摘要由大模型生成, 仅供参考"))

	return map[string]interface{}{
		"config":   map[string]interface{}{"wide_screen_mode": true},
		"header":   headerEl(title, "green"),
		"elements": elements,
	}
}

// windowElements renders one window: empty reminder or AI summary, then
// the announcement and forum pick sections.
func (s *Scheduler) windowElements(ctx context.Context, w *windowData) []map[string]interface{} {
	llmReady := s.Bot.LLM.Enabled()

	sortByTimeDesc(w.Announces)
	picked := s.pickForums(ctx, w.Forums, llmReady)

	var elements []map[string]interface{}

	if len(w.Announces) == 0 && len(picked) == 0 {
		elements = append(elements, mdElement("本时段没有新的公告或论坛内容。"))
	} else if summaryText, err := s.windowSummary(ctx, w, picked, llmReady); err == nil && summaryText != "" {
		elements = append(elements, mdElement(bot.SanitizeSummary(summaryText)))
	} else if err != nil {
		logrus.Warnf("digest stage: llm summary failed, degrading to list: %v", err)
		elements = append(elements, mdElement("AI 摘要暂不可用, 以下为原始条目列表。"))
	}
	elements = append(elements, divider())

	elements = append(elements, mdElement(fmt.Sprintf("**官网公告** (%d)", len(w.Announces))))
	if len(w.Announces) == 0 {
		elements = append(elements, mdElement("无"))
	}
	for i, a := range w.Announces {
		if i >= pickLimit {
			break
		}
		elements = append(elements, mdElement(fmt.Sprintf("- [%s](%s) (%s)",
			escape(a.Title), a.URL, time.UnixMilli(a.CreateTimeMS).In(time.Local).Format("01-02"))))
	}

	elements = append(elements, mdElement(fmt.Sprintf("**论坛精选** (%d)", len(picked))))
	if len(picked) == 0 {
		elements = append(elements, mdElement("无"))
	}
	for _, f := range picked {
		elements = append(elements, mdElement(fmt.Sprintf("- [%s](%s) · %s · %s",
			escape(f.Title), f.URL, escape(f.Source), escape(f.Author))))
	}
	return elements
}

// pickForums selects the most valuable forum posts with LLM help,
// falling back to newest-first.
func (s *Scheduler) pickForums(ctx context.Context, forums []rmsearch.Document, llmReady bool) []rmsearch.Document {
	if len(forums) <= pickLimit {
		return forums
	}
	picked := forums[:pickLimit]
	if !llmReady {
		return picked
	}
	t2 := time.Now()
	selections, err := selectPicks(ctx, s.Bot.LLM, forums, pickLimit)
	if err != nil {
		logrus.Warnf("digest stage: llm pick failed after %s, fallback to newest: %v",
			time.Since(t2).Round(time.Millisecond), err)
		return picked
	}
	var subset []rmsearch.Document
	for _, sel := range selections {
		if sel.Index >= 0 && sel.Index < len(forums) {
			subset = append(subset, forums[sel.Index])
		}
	}
	logrus.WithFields(logrus.Fields{
		"duration": time.Since(t2).Round(time.Millisecond).String(),
		"picked":   len(subset),
	}).Info("digest stage: llm pick done")
	if len(subset) > 0 {
		return subset
	}
	return picked
}

// windowSummary summarizes one window; empty windows skip the LLM
// entirely so the digest stays sub-second.
func (s *Scheduler) windowSummary(ctx context.Context, w *windowData, picked []rmsearch.Document, llmReady bool) (string, error) {
	if !llmReady {
		return "", fmt.Errorf("LLM 未配置")
	}
	t3 := time.Now()
	text, err := digestSummary(ctx, s.Bot.LLM, w.Start, w.End, w.Announces, picked)
	logrus.WithFields(logrus.Fields{
		"duration": time.Since(t3).Round(time.Millisecond).String(),
		"failed":   err != nil,
	}).Info("digest stage: llm summary done")
	return text, err
}

// selectPicks asks the LLM to choose the most valuable items from the
// candidate pool and return them as a JSON array.
func selectPicks(ctx context.Context, client *llm.Client, docs []rmsearch.Document, limit int) ([]selection, error) {
	var sb strings.Builder
	for i, d := range docs {
		sb.WriteString(fmt.Sprintf("%d. [%s] %s\n%s\n\n", i, d.Source, d.Title, clipRunes(d.Content, 150)))
	}
	system := "你是一个 RoboMaster 赛事社区编辑。从候选帖子中挑选对参赛者最有价值的条目, 优先: 官方重要通知、高质量技术开源、赛季关键信息、广泛关注的讨论。只输出 JSON 数组, 每项形如 {\"index\": 序号, \"reason\": \"十个字以内理由\"}, 不要输出其他内容。"
	user := fmt.Sprintf("请挑选最有价值的 %d 条:\n\n%s", limit, sb.String())

	resp, err := client.Chat(ctx, system, user)
	if err != nil {
		return nil, err
	}
	resp = strings.TrimSpace(resp)
	// Tolerate markdown code fences around the JSON.
	if i := strings.Index(resp, "["); i >= 0 {
		if j := strings.LastIndex(resp, "]"); j > i {
			resp = resp[i : j+1]
		}
	}
	var selections []selection
	if err := json.Unmarshal([]byte(resp), &selections); err != nil {
		return nil, fmt.Errorf("decode selection: %w", err)
	}
	sort.SliceStable(selections, func(a, b int) bool { return selections[a].Index < selections[b].Index })
	return selections, nil
}

// digestSummary generates the overview-then-bullets summary of a digest.
func digestSummary(ctx context.Context, client *llm.Client, start, end time.Time, announces, forums []rmsearch.Document) (string, error) {
	var sb strings.Builder
	sb.WriteString("官网公告:\n")
	for _, a := range announces {
		sb.WriteString(fmt.Sprintf("- %s: %s\n", a.Title, clipRunes(a.Content, 200)))
	}
	sb.WriteString("\n论坛帖子:\n")
	for _, f := range forums {
		sb.WriteString(fmt.Sprintf("- [%s] %s: %s\n", f.Source, f.Title, clipRunes(f.Content, 200)))
	}
	system := "你是一个 RoboMaster 赛事社区的日报编辑。根据给定材料写一段摘要: 先用 2-3 句话综述该时段最重要动态, 然后按公告和论坛分别用一句话列出每条要点。使用 Markdown, 不要使用标题, 内容使用中文, 总长控制在 300 字左右。"
	user := fmt.Sprintf("时段 %s ~ %s 的材料:\n\n%s",
		start.Format("01-02 15:04"), end.Format("01-02 15:04"), sb.String())
	return client.Chat(ctx, system, user)
}

func sortByTimeDesc(docs []rmsearch.Document) {
	sort.SliceStable(docs, func(a, b int) bool { return docs[a].CreateTimeMS > docs[b].CreateTimeMS })
}

func clipRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "..."
}

func escape(s string) string {
	r := strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;")
	return r.Replace(s)
}

func mdElement(content string) map[string]interface{} {
	return map[string]interface{}{"tag": "div", "text": map[string]interface{}{"tag": "lark_md", "content": content}}
}

func divider() map[string]interface{} {
	return map[string]interface{}{"tag": "hr"}
}

func headerEl(title, template string) map[string]interface{} {
	return map[string]interface{}{"template": template, "title": map[string]interface{}{"tag": "plain_text", "content": title}}
}

func noteEl(content string) map[string]interface{} {
	return map[string]interface{}{"tag": "note", "elements": []map[string]interface{}{mdElement(content)}}
}
