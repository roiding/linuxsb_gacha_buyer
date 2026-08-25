package web

import (
	"bytes"
	"encoding/json"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"gacha-buyer/internal/accounts"
	"gacha-buyer/internal/buyer"
	"gacha-buyer/internal/collector"
	"gacha-buyer/internal/config"
	"gacha-buyer/internal/db"
	"gacha-buyer/internal/lottery"
	"gacha-buyer/internal/store"
)

func newTestServer(t *testing.T) (*Server, *config.Config) {
	t.Helper()
	d, err := db.Open(filepath.Join(t.TempDir(), "web.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	cfg := config.Defaults()
	cfg.Subs = []config.SubAccount{{Username: "a@x.com", Password: "p", Enabled: true}}
	st := store.New(d)
	eng := buyer.New(&cfg, st, nil)
	mgr, err := accounts.New(&cfg, d, nil)
	if err != nil {
		t.Fatal(err)
	}
	col := collector.New(&cfg, mgr, d, nil)
	lot := lottery.New(&cfg, mgr, d, nil)
	return New(&cfg, d, st, eng, mgr, col, lot), &cfg
}

func TestCollectorPayloadHasNoWindowFields(t *testing.T) {
	s, _ := newTestServer(t)
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/api/accounts", nil))
	if rec.Code != 200 {
		t.Fatalf("GET /api/accounts: %d %s", rec.Code, rec.Body.String())
	}
	var body struct {
		Collector map[string]any `json:"collector"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Collector == nil {
		t.Fatal("缺少 collector 载荷")
	}
	if _, ok := body.Collector["at_hour"]; ok {
		t.Fatal("at_hour 不应再返回")
	}
	if _, ok := body.Collector["random_window_min"]; ok {
		t.Fatal("random_window_min 不应再返回")
	}
	if body.Collector["min_tip"] == nil {
		t.Fatal("min_tip 应返回")
	}
}

func TestCollectorPostAndTransfersPlans(t *testing.T) {
	s, cfg := newTestServer(t)

	body := `{"collector":{"topic_id":7,"keep":3,"min_tip":2,"message":"hi"}}`
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, httptest.NewRequest("POST", "/api/accounts", bytes.NewBufferString(body)))
	if rec.Code != 200 {
		t.Fatalf("POST /api/accounts: %d %s", rec.Code, rec.Body.String())
	}
	if cfg.Collector.TopicID != 7 || cfg.Collector.Keep != 3 || cfg.Collector.MinTip != 2 || cfg.Collector.Message != "hi" {
		t.Fatalf("collector 配置未生效: %+v", cfg.Collector)
	}

	rec = httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/api/transfers", nil))
	if rec.Code != 200 {
		t.Fatalf("GET /api/transfers: %d %s", rec.Code, rec.Body.String())
	}
	var out struct {
		Status struct {
			Plans []struct {
				Account   string `json:"account"`
				PlannedAt string `json:"planned_at"`
				Status    string `json:"status"`
			} `json:"plans"`
		} `json:"status"`
		Gacha []any `json:"gacha"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if out.Gacha == nil {
		t.Fatal("/api/transfers 应返回 gacha 数组")
	}
	if len(out.Status.Plans) != 1 {
		t.Fatalf("status.plans 应含 1 条计划: %+v", out.Status.Plans)
	}
	p := out.Status.Plans[0]
	if p.PlannedAt == "" || p.Status != "planned" {
		t.Fatalf("计划字段错误: %+v", p)
	}
}
