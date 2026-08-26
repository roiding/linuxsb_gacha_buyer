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

func TestTargetsNormalize(t *testing.T) {
	cfg := Defaults()
	cfg.Targets = map[string]TargetRule{
		"欧皇":  {Price: 200, Max: 3},
		"夜猫子": {Price: 0, Max: 0},   // 无效：价格与数量都为空
		"路人甲": {Price: 0, Max: 5},   // 仅限制数量
		"潜水员": {Price: 66, Max: -1}, // Max 负值纠正为 0
		"   ": {Price: 1, Max: 1},
		"":    {Price: 1, Max: 1},
	}
	cfg.Normalize()
	if _, ok := cfg.Targets["欧皇"]; !ok {
		t.Fatalf("欧皇应保留: %+v", cfg.Targets)
	}
	if _, ok := cfg.Targets["夜猫子"]; ok {
		t.Fatalf("价格与数量都为空应删除: %+v", cfg.Targets)
	}
	if _, ok := cfg.Targets["路人甲"]; !ok {
		t.Fatalf("仅限制数量应保留: %+v", cfg.Targets)
	}
	if r := cfg.Targets["潜水员"]; r.Max != 0 || r.Price != 66 {
		t.Fatalf("负 Max 应纠正为 0: %+v", r)
	}
	if _, ok := cfg.Targets["   "]; ok {
		t.Fatalf("纯空白名称应删除: %+v", cfg.Targets)
	}
	if _, ok := cfg.Targets[""]; ok {
		t.Fatalf("空名称应删除: %+v", cfg.Targets)
	}
}
