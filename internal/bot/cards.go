package bot

import (
	"context"
	"fmt"
	"html"
	"strings"
	"time"

	"github.com/unnc-aim/aim-feishu-rm-assistant/internal/llm"
	"github.com/unnc-aim/aim-feishu-rm-assistant/internal/rmsearch"
	"github.com/unnc-aim/aim-feishu-rm-assistant/internal/store"
)

// ---------- helpers ----------

func mdText(content string) map[string]interface{} {
	return map[string]interface{}{"tag": "lark_md", "content": content}
}

func plainText(content string) map[string]interface{} {
	return map[string]interface{}{"tag": "plain_text", "content": content}
}

func header(title, template string) map[string]interface{} {
	return map[string]interface{}{
		"template": template,
		"title":    plainText(title),
	}
}

func note(content string) map[string]interface{} {
	return map[string]interface{}{
		"tag":      "note",
		"elements": []map[string]interface{}{mdText(content)},
	}
}

func mdElement(content string) map[string]interface{} {
	return map[string]interface{}{"tag": "div", "text": mdText(content)}
}

// escape protects user and remote content inside lark_md.
func escape(s string) string {
	return html.EscapeString(s)
}

func clip(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "..."
}

// SanitizeSummary strips markdown emphasis the model tends to emit.
// lark_md renders *word* and _word_ as italics, which reads as random
// slanted text; bold pairs (**) are kept, lone asterisks and underscores
// are neutralized so summaries render as plain text.
func SanitizeSummary(s string) string {
	const bold = "\x00"
	s = strings.ReplaceAll(s, "**", bold)
	s = strings.ReplaceAll(s, "*", "")
	s = strings.ReplaceAll(s, "_", "\\_")
	return strings.ReplaceAll(s, bold, "**")
}

// prependAtElement inserts an at markup div in front of the card content.
func prependAtElement(card map[string]any, at string) {
	elements, ok := card["elements"].([]map[string]any)
	if !ok || len(elements) == 0 {
		return
	}
	card["elements"] = append([]map[string]any{mdElement(at)}, elements...)
}

// ---------- cards ----------

// SearchResultCard renders one page of search hits with pagination hints.
func SearchResultCard(query string, res *rmsearch.SearchResult, offset, pageSize int, summaryOn bool) map[string]interface{} {
	var sb strings.Builder
	for i, hit := range res.Hits {
		no := offset + i + 1
		sb.WriteString(fmt.Sprintf("**%d. [%s](%s)**\n", no, escape(hit.Title), hit.URL))
		sb.WriteString(fmt.Sprintf("%s · %s · %s\n\n",
			escape(hit.Source), escape(displayTime(hit.CreateTimeMS)), escape(hit.Author)))
	}

	total := res.Total
	if total > 1000 {
		total = 1000 // Meilisearch estimatedTotalHits caps at 1000
	}
	summaryState := "开启"
	if !summaryOn {
		summaryState = "关闭"
	}

	page := offset/pageSize + 1
	elements := []map[string]interface{}{
		mdElement(sb.String()),
		note(fmt.Sprintf(
			"第 %d 页 · 第 %d-%d 条, 共 %d 条 · 发送“下一页/上一页/第N页”翻页 · AI总结: %s",
			page, offset+1, offset+len(res.Hits), total, summaryState)),
	}

	return map[string]interface{}{
		"config":   map[string]interface{}{"wide_screen_mode": true},
		"header":   header("RM Search 结果", "blue"),
		"elements": elements,
	}
}

// EmptyResultCard is sent when a query has no hits.
func EmptyResultCard(query string) map[string]interface{} {
	return map[string]interface{}{
		"config": map[string]interface{}{"wide_screen_mode": true},
		"header": header("RM Search 结果", "grey"),
		"elements": []map[string]interface{}{
			mdElement(fmt.Sprintf("没有找到与 **%s** 相关的内容, 换个关键词试试?", escape(query))),
		},
	}
}

// SummaryCard renders the async LLM summary follow-up of a search. Summary
// failures are retried with backoff and then only logged, so there is no
// failure variant of this card.
func SummaryCard(query, summary string) map[string]interface{} {
	return map[string]interface{}{
		"config": map[string]interface{}{"wide_screen_mode": true},
		"header": header("AI 总结", "turquoise"),
		"elements": []map[string]interface{}{
			mdElement(SanitizeSummary(summary)),
			note("由大模型生成, 仅供参考: " + escape(query)),
		},
	}
}

// SettingsCard shows current settings of a chat and the accepted commands.
func SettingsCard(st *store.Settings) map[string]interface{} {
	var status string
	switch {
	case !st.Subscribed:
		status = "未订阅"
	case st.Frequency == store.FrequencyWeekly:
		status = fmt.Sprintf("已订阅每周推送 (每周一 %02d:%02d)", st.PushHour, st.PushMinute)
	default:
		status = fmt.Sprintf("已订阅每日推送 (每天 %02d:%02d)", st.PushHour, st.PushMinute)
	}
	summaryState := "开启"
	if !st.SummaryOn {
		summaryState = "关闭"
	}

	return map[string]interface{}{
		"config": map[string]interface{}{"wide_screen_mode": true},
		"header": header("助手设置", "violet"),
		"elements": []map[string]interface{}{
			mdElement(fmt.Sprintf("**推送状态**: %s\n**搜索AI总结**: %s", status, summaryState)),
			note("可发送: 订阅每日推送 / 订阅每周推送 / 订阅每天晚上9点推送 / 退订 / 开启总结 / 关闭总结"),
		},
	}
}

// HelpCard explains what the bot can do.
func HelpCard() map[string]interface{} {
	return map[string]interface{}{
		"config": map[string]interface{}{"wide_screen_mode": true},
		"header": header("RM 助手", "blue"),
		"elements": []map[string]interface{}{
			mdElement("**搜索**\n直接发送关键词 (群聊需要 @机器人), 返回论坛文章、问答、专栏、官网公告和 PDF 附件的搜索结果。发送“下一页”“上一页”“第N页”翻页。\n\n" +
				"**AI 总结**\n搜索结果之后会跟一条 AI 总结, 可发“关闭总结”停用、“开启总结”恢复。\n\n" +
				"**定时推送**\n发“订阅每日推送”或“订阅每周推送”接收定期精选摘要, 支持自定义时间, 例如“订阅每天晚上9点推送”。每周推送在周一发送, 覆盖上个自然周。发“退订”停止推送。"),
			note("数据来源: RM Search (search.scutbot.cn)"),
		},
	}
}

// displayTime formats a unix millisecond timestamp for cards.
func displayTime(ms int64) string {
	if ms <= 0 {
		return "未知时间"
	}
	return time.UnixMilli(ms).In(time.Local).Format("2006-01-02")
}

// SummarizeSearch asks the LLM for a digest of search hits in the
// "overview then bullet list" style. It returns lark_md ready text.
func SummarizeSearch(ctx context.Context, client *llm.Client, query string, hits []rmsearch.Document) (string, error) {
	var sb strings.Builder
	for i, hit := range hits {
		sb.WriteString(fmt.Sprintf("%d. [%s] %s\n%s\n\n", i+1, hit.Source, hit.Title, clip(hit.Content, 300)))
	}
	system := "你是一个 RoboMaster 赛事社区的内容助手。用户给出搜索关键词和搜索结果, 请先写 2-3 句总体综述, 然后逐条用一句话总结每条结果的核心信息。使用 Markdown 列表, 不要使用标题, 内容使用中文。"
	user := fmt.Sprintf("搜索关键词: %s\n\n搜索结果:\n%s", query, sb.String())
	return client.Chat(ctx, system, user)
}
