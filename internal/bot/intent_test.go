package bot

import (
	"testing"
	"time"

	larkim "github.com/larksuite/oapi-sdk-go/v3/service/im/v1"

	"github.com/unnc-aim/aim-feishu-rm-assistant/internal/store"
)

// newMentionList builds a mention event list with the given keys.
func newMentionList(keys ...string) []*larkim.MentionEvent {
	var ret []*larkim.MentionEvent
	for _, k := range keys {
		key := k
		ret = append(ret, &larkim.MentionEvent{Key: &key})
	}
	return ret
}

func TestParseIntent(t *testing.T) {
	cases := []struct {
		text    string
		kind    IntentKind
		hour    int
		min     int
		hasTime bool
	}{
		{"退订", IntentUnsubscribe, 0, 0, false},
		{"取消订阅", IntentUnsubscribe, 0, 0, false},
		{"不要推送了", IntentUnsubscribe, 0, 0, false},
		{"开启总结", IntentSummaryOn, 0, 0, false},
		{"关闭总结", IntentSummaryOff, 0, 0, false},
		{"订阅每日推送", IntentSubscribeDaily, 0, 0, false},
		{"订阅每周推送", IntentSubscribeWeekly, 0, 0, false},
		{"订阅每月推送", IntentSubscribeMonthly, 0, 0, false},
		{"每月1号9点推送", IntentSubscribeMonthly, 9, 0, true},
		{"订阅月报", IntentSubscribeMonthly, 0, 0, false},
		{"订阅每天晚上9点推送", IntentSubscribeDaily, 21, 0, true},
		{"每天晚上8点半推送", IntentSubscribeDaily, 20, 30, true},
		{"订阅周报 早上9点", IntentSubscribeWeekly, 9, 0, true},
		{"订阅下午三点推送", IntentSubscribeDaily, 15, 0, true},
		{"视觉识别", IntentNone, 0, 0, false},
		{"如何调pid", IntentNone, 0, 0, false},
	}
	for _, c := range cases {
		got := ParseIntent(c.text)
		if got.Kind != c.kind {
			t.Errorf("ParseIntent(%q) kind = %v, want %v", c.text, got.Kind, c.kind)
			continue
		}
		if got.HasTime != c.hasTime || got.Hour != c.hour || got.Minute != c.min {
			t.Errorf("ParseIntent(%q) time = (%d,%d,%v), want (%d,%d,%v)",
				c.text, got.Hour, got.Minute, got.HasTime, c.hour, c.min, c.hasTime)
		}
	}
}

func TestExtractText(t *testing.T) {
	content := `{"text":"@_user_1 视觉识别"}`
	got := extractText(&content, newMentionList("@_user_1"))
	if got != "视觉识别" {
		t.Errorf("extractText = %q, want %q", got, "视觉识别")
	}
}

func TestSummaryRetryDelay(t *testing.T) {
	want := []time.Duration{5, 10, 20, 40, 80, 120, 120, 120, 120, 120}
	for i, w := range want {
		if got := summaryRetryDelay(i); got != w*time.Second {
			t.Errorf("summaryRetryDelay(%d) = %v, want %v", i, got, w*time.Second)
		}
	}
}

func TestSanitizeSummary(t *testing.T) {
	cases := []struct{ in, want string }{
		{"plain text", "plain text"},
		{"*italic* must go", "italic must go"},
		{"**bold** stays", "**bold** stays"},
		{"snake_case_word", `snake\_case\_word`},
		{"*one* and **two** mixed", "one and **two** mixed"},
		{"contains <div> tag", "contains ＜div＞ tag"},
		{"angle <0.05 ok", "angle ＜0.05 ok"},
	}
	for _, c := range cases {
		if got := SanitizeSummary(c.in); got != c.want {
			t.Errorf("SanitizeSummary(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestMentionsSelf(t *testing.T) {
	self := "ou_self"
	openID := func(s string) *larkim.MentionEvent {
		id := s
		return &larkim.MentionEvent{Id: &larkim.UserId{OpenId: &id}}
	}
	mentions := []*larkim.MentionEvent{openID("ou_other"), openID("ou_self")}
	if !mentionsSelf(mentions, self) {
		t.Error("mentionsSelf should find the bot")
	}
	if mentionsSelf(mentions[:1], self) {
		t.Error("mentionsSelf must not match other users")
	}
	if mentionsSelf(nil, self) {
		t.Error("mentionsSelf on empty list must be false")
	}
}

func TestPrependAtElement(t *testing.T) {
	card := map[string]any{
		"elements": []map[string]any{
			{"tag": "div", "text": map[string]any{"content": "body"}},
		},
	}
	prependAtElement(card, "<at user_id=\"ou_x\"></at>")
	elements := card["elements"].([]map[string]any)
	if len(elements) != 2 {
		t.Fatalf("elements len = %d, want 2", len(elements))
	}
	first := elements[0]["text"].(map[string]any)["content"].(string)
	if first != "<at user_id=\"ou_x\"></at>" {
		t.Errorf("first element content = %q", first)
	}
}

func TestManualDigestFreq(t *testing.T) {
	cases := []struct {
		in   string
		freq string
		ok   bool
	}{
		{"测试推送", store.FrequencyDaily, true},
		{"立即推送", store.FrequencyDaily, true},
		{"测试周推送", store.FrequencyWeekly, true},
		{"测试每周推送", store.FrequencyWeekly, true},
		{"测试月推送", store.FrequencyMonthly, true},
		{"测试每月推送", store.FrequencyMonthly, true},
		{"测试月报", store.FrequencyMonthly, true},
		{"云台", "", false},
		{"帮我测试一下", "", false},
	}
	for _, c := range cases {
		freq, ok := manualDigestFreq(c.in)
		if freq != c.freq || ok != c.ok {
			t.Errorf("manualDigestFreq(%q) = (%q, %v), want (%q, %v)", c.in, freq, ok, c.freq, c.ok)
		}
	}
}
