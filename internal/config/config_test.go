package config

import (
	"encoding/json"
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

func TestCollectorDefaultsAndNormalizeWithoutWindowFields(t *testing.T) {
	cfg := Defaults()
	if cfg.Collector.Keep != 5 || cfg.Collector.MinTip != 1 {
		t.Fatalf("默认归集配置错误: %+v", cfg.Collector)
	}
	cfg.Collector.Keep = -3
	cfg.Collector.MinTip = 0
	cfg.Normalize()
	if cfg.Collector.Keep != 0 || cfg.Collector.MinTip != 1 {
		t.Fatalf("Normalize 纠错失败: %+v", cfg.Collector)
	}
	// 旧配置里残留的窗口字段应被 JSON 解析忽略。
	raw := `{"collector":{"topic_id":3,"keep":5,"at_hour":9,"random_window_min":60,"message":"x","min_tip":2}}`
	var c Config
	if err := json.Unmarshal([]byte(raw), &c); err != nil {
		t.Fatal(err)
	}
	if c.Collector.TopicID != 3 || c.Collector.MinTip != 2 || c.Collector.Message != "x" {
		t.Fatalf("其余字段应正常解析: %+v", c.Collector)
	}
}
