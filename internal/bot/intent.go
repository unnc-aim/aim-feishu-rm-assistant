package bot

import (
	"regexp"
	"strconv"
	"strings"
)

// IntentKind enumerates user intents expressible in natural language.
type IntentKind int

const (
	IntentNone IntentKind = iota
	IntentSubscribeDaily
	IntentSubscribeWeekly
	IntentUnsubscribe
	IntentSummaryOn
	IntentSummaryOff
	IntentSubscribeMonthly
)

// Intent is a parsed user command about subscription or settings.
type Intent struct {
	Kind    IntentKind
	Hour    int
	Minute  int
	HasTime bool
}

var (
	// timePattern matches "8点", "8:30", "8点半", "20时15".
	timePattern = regexp.MustCompile(`(\d{1,2})\s*[点时:：]\s*(半|\d{1,2})?`)
)

// ParseIntent recognizes subscription/settings phrases from free text.
// It returns IntentNone when the text should be treated as a search query.
func ParseIntent(text string) *Intent {
	t := strings.TrimSpace(text)

	if containsAny(t, []string{"退订", "取消订阅", "取消推送", "不要推送", "关闭推送", "停止推送", "不接收推送"}) {
		return &Intent{Kind: IntentUnsubscribe}
	}

	summaryOn := containsAny(t, []string{"开启总结", "打开总结", "开启摘要", "打开摘要", "开启ai总结", "打开ai总结", "开启AI总结"})
	summaryOff := containsAny(t, []string{"关闭总结", "关掉总结", "关闭摘要", "关掉摘要", "不要总结", "关闭ai总结", "关掉ai总结", "关闭AI总结"})
	if summaryOn && !summaryOff {
		return &Intent{Kind: IntentSummaryOn}
	}
	if summaryOff {
		return &Intent{Kind: IntentSummaryOff}
	}

	wantsPush := containsAny(t, []string{"订阅", "推送", "日报", "周报", "定时"})
	if !wantsPush {
		return &Intent{Kind: IntentNone}
	}

	intent := &Intent{}
	intent.Hour, intent.Minute, intent.HasTime = parseTimeOfDay(t)

	monthly := containsAny(t, []string{"每月", "每个月", "月报"})
	weekly := containsAny(t, []string{"每周", "礼拜", "周报", "一周"})
	daily := containsAny(t, []string{"每日", "每天", "日报", "日常"})
	if monthly {
		intent.Kind = IntentSubscribeMonthly
		return intent
	}
	if weekly && !daily {
		intent.Kind = IntentSubscribeWeekly
		return intent
	}
	intent.Kind = IntentSubscribeDaily
	return intent
}

// parseTimeOfDay extracts a time of day from phrases such as
// "晚上8点", "20:30", "下午三点" (Chinese numerals limited to 一..九).
func parseTimeOfDay(t string) (hour, minute int, ok bool) {
	// Normalize a few Chinese numerals for the hour.
	replacer := strings.NewReplacer(
		"零", "0", "一", "1", "二", "2", "两", "2", "三", "3", "四", "4",
		"五", "5", "六", "6", "七", "7", "八", "8", "九", "9", "十", "10",
	)
	normalized := replacer.Replace(t)

	m := timePattern.FindStringSubmatch(normalized)
	if m == nil {
		return 0, 0, false
	}
	hour, err := strconv.Atoi(m[1])
	if err != nil || hour < 0 || hour > 23 {
		return 0, 0, false
	}
	switch {
	case m[2] == "半":
		minute = 30
	case m[2] != "":
		minute, err = strconv.Atoi(m[2])
		if err != nil || minute < 0 || minute > 59 {
			return 0, 0, false
		}
	}

	// 12-hour style prefixes shift the hour into the afternoon/evening.
	isPM := strings.Contains(t, "下午") || strings.Contains(t, "傍晚") ||
		strings.Contains(t, "晚上") || strings.Contains(t, "今晚") || strings.Contains(t, "夜晚")
	if isPM && hour < 12 {
		hour += 12
	}
	if strings.Contains(t, "中午") && hour < 12 {
		hour += 12
	}
	return hour, minute, true
}

func containsAny(s string, subs []string) bool {
	for _, sub := range subs {
		if strings.Contains(s, sub) {
			return true
		}
	}
	return false
}
