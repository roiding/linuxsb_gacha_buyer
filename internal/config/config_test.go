package config

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestNormalizeTopicURLAndDefaultMessages(t *testing.T) {
	got, err := NormalizeTopicURL("https://linux.sb/", "https://linux.sb/topic/15458")
	if err != nil || got != "https://linux.sb/topic/15458" {
		t.Fatalf("规范 URL 失败: %q %v", got, err)
	}
	for _, bad := range []string{
		"https://example.com/topic/15458",
		"https://linux.sb/topic/15458?p=1",
		"https://linux.sb/topic/0",
		"https://linux.sb/user/7",
	} {
		if _, err := NormalizeTopicURL("https://linux.sb", bad); err == nil {
			t.Errorf("非法 URL 应被拒绝: %s", bad)
		}
	}
	messages := DefaultLotteryMessages()
	if len(messages) < 40 {
		t.Fatalf("默认语料不足 40 条: %d", len(messages))
	}
	seen := map[string]bool{}
	for _, msg := range messages {
		if utf8.RuneCountInString(strings.TrimSpace(msg)) < 5 || seen[msg] {
			t.Fatalf("默认语料无效或重复: %q", msg)
		}
		seen[msg] = true
	}
}
