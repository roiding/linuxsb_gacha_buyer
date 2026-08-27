package web

import (
	"bytes"
	"encoding/json"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

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
	eng.Mgr = mgr // 主号未配置时快速失败，避免测试发真实登录请求
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

func TestConfigPostTriggersDeepScanHint(t *testing.T) {
	s, _ := newTestServer(t)
	rec := httptest.NewRecorder()
	body := `{"rules":{"sr":30,"r":10,"n":4},"scan_sec":5}`
	s.Handler().ServeHTTP(rec, httptest.NewRequest("POST", "/api/config", bytes.NewBufferString(body)))
	if rec.Code != 200 {
		t.Fatalf("POST /api/config: %d %s", rec.Code, rec.Body.String())
	}
	var out struct {
		OK      bool   `json:"ok"`
		Message string `json:"message"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if !out.OK {
		t.Fatalf("保存配置应成功: %+v", out)
	}
	// 测试环境未配主号，深度收购会给出未触发提示；正式环境配了主号会后台开始扫描。
	if !strings.Contains(out.Message, "深度收购") {
		t.Fatalf("保存采购设置后应返回深度收购提示: %q", out.Message)
	}
}

func TestMarketPublishCancelRoutes(t *testing.T) {
	s, _ := newTestServer(t)

	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, httptest.NewRequest("POST", "/api/market/publish",
		bytes.NewBufferString(`{"rarities":["n","r","sr"],"unit_price":66,"duration_hours":24}`)))
	if rec.Code != 200 {
		t.Fatalf("POST /api/market/publish: %d %s", rec.Code, rec.Body.String())
	}
	var pub struct {
		Success int `json:"success"`
		Failed  int `json:"failed"`
		Items   []struct {
			Message string `json:"message"`
		} `json:"items"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &pub); err != nil {
		t.Fatal(err)
	}
	if pub.Failed != 1 || len(pub.Items) != 1 {
		t.Fatalf("未配主号时应返回 1 条失败提示: %+v", pub)
	}

	rec = httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, httptest.NewRequest("POST", "/api/market/cancel", nil))
	if rec.Code != 200 {
		t.Fatalf("POST /api/market/cancel: %d %s", rec.Code, rec.Body.String())
	}
	var can struct {
		Items []struct {
			Message string `json:"message"`
		} `json:"items"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &can); err != nil {
		t.Fatal(err)
	}
	if len(can.Items) != 1 {
		t.Fatalf("未配主号时应返回 1 条失败提示: %+v", can)
	}
}

func TestConfigScanModeRoundTrip(t *testing.T) {
	s, _ := newTestServer(t)
	get := func() string {
		t.Helper()
		rec := httptest.NewRecorder()
		s.Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/api/config", nil))
		if rec.Code != 200 {
			t.Fatalf("GET /api/config: %d %s", rec.Code, rec.Body.String())
		}
		var out struct {
			ScanMode string `json:"scan_mode"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
			t.Fatal(err)
		}
		return out.ScanMode
	}

	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, httptest.NewRequest("POST", "/api/config",
		bytes.NewBufferString(`{"scan_mode":"fast"}`)))
	if rec.Code != 200 {
		t.Fatalf("POST /api/config scan_mode: %d %s", rec.Code, rec.Body.String())
	}
	if got := get(); got != "fast" {
		t.Fatalf("scan_mode 应保存为 fast: %q", got)
	}

	// 非法值应回落为空（自动）
	rec = httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, httptest.NewRequest("POST", "/api/config",
		bytes.NewBufferString(`{"scan_mode":"turbo"}`)))
	if rec.Code != 200 {
		t.Fatalf("POST /api/config 非法 scan_mode: %d %s", rec.Code, rec.Body.String())
	}
	if got := get(); got != "" {
		t.Fatalf("非法 scan_mode 应回落为空: %q", got)
	}
}

func TestPurchasesPagination(t *testing.T) {
	s, _ := newTestServer(t)
	for i := 1; i <= 5; i++ {
		if err := s.st.Add(store.Purchase{Time: time.Now(), ListingID: i, Name: "卡", Price: i, Qty: 1, Cost: i}); err != nil {
			t.Fatal(err)
		}
	}
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/api/purchases?page=2&page_size=2", nil))
	if rec.Code != 200 {
		t.Fatalf("GET /api/purchases: %d %s", rec.Code, rec.Body.String())
	}
	var out struct {
		Records    []map[string]any `json:"records"`
		Total      int              `json:"total"`
		Page       int              `json:"page"`
		PageSize   int              `json:"page_size"`
		TotalPages int              `json:"total_pages"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if out.Total != 5 || out.Page != 2 || out.PageSize != 2 || out.TotalPages != 3 || len(out.Records) != 2 {
		t.Fatalf("分页元数据错误: total=%d page=%d size=%d pages=%d rows=%d",
			out.Total, out.Page, out.PageSize, out.TotalPages, len(out.Records))
	}

	// 越界页返回空列表但元数据正确
	rec = httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/api/purchases?page=9&page_size=2", nil))
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if out.Total != 5 || out.TotalPages != 3 || len(out.Records) != 0 {
		t.Fatalf("越界页错误: total=%d pages=%d rows=%d", out.Total, out.TotalPages, len(out.Records))
	}
}

func TestTransfersPagination(t *testing.T) {
	s, _ := newTestServer(t)
	for i := 1; i <= 3; i++ {
		if err := s.d.AddTransfer(&db.TransferRow{Time: time.Now(), AccountID: i, Sub: "小号", TipAmount: i}); err != nil {
			t.Fatal(err)
		}
	}
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/api/transfers?page=2&page_size=2", nil))
	if rec.Code != 200 {
		t.Fatalf("GET /api/transfers: %d %s", rec.Code, rec.Body.String())
	}
	var out struct {
		Transfers  []map[string]any `json:"transfers"`
		Total      int              `json:"total"`
		Page       int              `json:"page"`
		TotalPages int              `json:"total_pages"`
		Status     map[string]any   `json:"status"`
		Gacha      []any            `json:"gacha"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if out.Total != 3 || out.Page != 2 || out.TotalPages != 2 || len(out.Transfers) != 1 {
		t.Fatalf("归集分页错误: total=%d page=%d pages=%d rows=%d",
			out.Total, out.Page, out.TotalPages, len(out.Transfers))
	}
	if out.Status == nil || out.Gacha == nil {
		t.Fatal("status/gacha 载荷应保留")
	}
}

func TestConfigTargetsRoundTrip(t *testing.T) {
	s, cfg := newTestServer(t)

	// POST 保存定向规则
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, httptest.NewRequest("POST", "/api/config",
		bytes.NewBufferString(`{"targets":{"论坛之星":{"price":88,"max":3},"路人甲":{"price":0,"max":5}}}`)))
	if rec.Code != 200 {
		t.Fatalf("POST /api/config targets: %d %s", rec.Code, rec.Body.String())
	}

	// GET 读回
	rec = httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/api/config", nil))
	if rec.Code != 200 {
		t.Fatalf("GET /api/config: %d %s", rec.Code, rec.Body.String())
	}
	var out struct {
		Targets map[string]config.TargetRule `json:"targets"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if out.Targets["论坛之星"].Price != 88 || out.Targets["论坛之星"].Max != 3 {
		t.Fatalf("定向规则保存/读取错误: %+v", out.Targets)
	}
	if out.Targets["路人甲"].Max != 5 {
		t.Fatalf("仅限数量的规则应保留: %+v", out.Targets)
	}
	_ = cfg
}
