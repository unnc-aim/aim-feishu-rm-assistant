package bot

import (
	"testing"

	larkim "github.com/larksuite/oapi-sdk-go/v3/service/im/v1"
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
