package buyer

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"gacha-buyer/internal/config"
	"gacha-buyer/internal/db"
	"gacha-buyer/internal/market"
	"gacha-buyer/internal/site"
	"gacha-buyer/internal/store"
)

func mkEngine(t *testing.T, rules config.PriceRules) *Engine {
	t.Helper()
	cfg := config.Defaults()
	cfg.Rules = rules
	d, err := db.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	return New(&cfg, store.New(d), nil)
}

func TestMatchRulesAndQuantity(t *testing.T) {
	e := mkEngine(t, config.PriceRules{SR: 30, R: 10, N: 4})
	all := []market.Listing{
		{ListingID: 1, Name: "万人迷", Rarity: market.SR, Price: 30, Remain: 3, CSRF: "x"},
		{ListingID: 2, Name: "欧皇", Rarity: market.SSR, Price: 200, Remain: 5, CSRF: "x"},
		{ListingID: 3, Name: "夜猫子", Rarity: market.R, Price: 11, Remain: 10, CSRF: "x"},
		{ListingID: 4, Name: "夜猫子", Rarity: market.R, Price: 10, Remain: 30, CSRF: "x"},
		{ListingID: 5, Name: "潜水员", Rarity: market.N, Price: 4, Remain: 1, CSRF: "x"},
	}
	got := e.match(all)
	if len(got) != 3 {
		t.Fatalf("期望命中 3 条，得到 %d: %+v", len(got), got)
	}
	if got[1].ListingID != 4 || got[1].Remain != 30 {
		t.Errorf("listing4 数量=%d，want 30", got[1].Remain)
	}
}

func TestMatchSSRByName(t *testing.T) {
	e := mkEngine(t, config.PriceRules{SSR: 999})
	e.cfg.SSRPrices = map[string]int{"欧皇": 200}
	all := []market.Listing{
		{ListingID: 1, Name: "欧皇", Rarity: market.SSR, Price: 200, Remain: 1},
		{ListingID: 2, Name: "氪金大佬", Rarity: market.SSR, Price: 1, Remain: 1},
		{ListingID: 3, Name: "欧皇", Rarity: market.SSR, Price: 201, Remain: 1},
	}
	got := e.match(all)
	if len(got) != 1 || got[0].Name != "欧皇" {
		t.Fatalf("SSR 定向匹配错误: %+v", got)
	}
}

func TestMatchZeroLimitSkips(t *testing.T) {
	e := mkEngine(t, config.PriceRules{SR: 30})
	all := []market.Listing{
		{ListingID: 6, Name: "夜猫子", Rarity: market.R, Price: 5, Remain: 9},
		{ListingID: 7, Name: "潜水员", Rarity: market.N, Price: 2, Remain: 9},
	}
	if got := e.match(all); len(got) != 0 {
		t.Fatalf("未配置稀有度不应买: %+v", got)
	}
}

func TestActiveRarities(t *testing.T) {
	cfg := config.Defaults()
	cfg.Rules = config.PriceRules{N: 4, R: 10, SR: 30, UR: 0} // UR 限价为 0 不采购
	cfg.SSRPrices = map[string]int{"欧皇": 200}

	got := activeRarities(&cfg)
	want := []market.Rarity{market.N, market.R, market.SR, market.SSR}
	if len(got) != len(want) {
		t.Fatalf("启用分类数量错误: %v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("分类顺序错误: got=%v want=%v", got, want)
		}
	}

	cfg.SSRPrices = nil
	got = activeRarities(&cfg)
	if len(got) != 3 || got[0] != market.N || got[1] != market.R || got[2] != market.SR {
		t.Fatalf("无 SSR 价格时不应包含 SSR 分类: %v", got)
	}

	none := config.Defaults()
	none.Rules = config.PriceRules{}
	none.SSRPrices = nil
	if got := activeRarities(&none); len(got) != 0 {
		t.Fatalf("全部限价为 0 时不应启用任何分类: %v", got)
	}
}

func TestRaritiesWithTargets(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`
			<div class="gacha-all-item"><span class="gacha-title-badge gacha-title-n">
				<span class="gacha-title-name">潜水员</span></span></div>
			<div class="gacha-all-item"><span class="gacha-title-badge gacha-title-r">
				<span class="gacha-title-name">话题王</span></span></div>
			<div class="gacha-all-item"><span class="gacha-title-badge gacha-title-ur">
				<span class="gacha-title-name">UR卡</span></span></div>`))
	}))
	defer server.Close()

	cfg := config.Defaults()
	cfg.Rules = config.PriceRules{N: 4} // 仅启用 N；R 和 UR 类型限价为 0
	cfg.Site = server.URL
	sc, err := site.NewClient(&cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	e := mkEngine(t, cfg.Rules)
	e.cfg = &cfg
	// 定向"话题王"(R) 和 "UR卡"(UR)，类型限价虽为 0 也应补入扫描分类
	cfg.Targets = map[string]config.TargetRule{
		"话题王": {Price: 10, Max: 2},
		"UR卡": {Price: 5, Max: 1},
	}
	base := activeRarities(&cfg) // [N]
	got := e.raritiesWithTargets(sc, base, &cfg)
	if len(got) != 3 {
		t.Fatalf("应补入 R 和 UR: got=%v", got)
	}
	want := []market.Rarity{market.N, market.R, market.UR}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("分类顺序错误: got=%v want=%v", got, want)
		}
	}
}

func TestPublishCandidates(t *testing.T) {
	catalog := []market.Title{
		{Name: "欧皇", Rarity: market.SSR},
		{Name: "打酱油的", Rarity: market.N},
		{Name: "话题王", Rarity: market.R},
		{Name: "论坛之星", Rarity: market.SR},
	}
	publishable := []site.PublishableTitle{
		{TitleID: 16, Name: "欧皇", Sellable: 1},
		{TitleID: 5, Name: "打酱油的", Sellable: 2},
		{TitleID: 8, Name: "话题王", Sellable: 1},
		{TitleID: 13, Name: "论坛之星", Sellable: 1},
		{TitleID: 99, Name: "未知称号", Sellable: 3},
	}
	got := publishCandidates(publishable, catalog, map[market.Rarity]bool{
		market.N:  true,
		market.SR: true,
	})
	if len(got) != 2 {
		t.Fatalf("应筛出 N+SR 共 2 个: %+v", got)
	}
	if got[0].Name != "打酱油的" || got[0].Sellable != 2 {
		t.Fatalf("打酱油的候选错误: %+v", got[0])
	}
	if got[1].Name != "论坛之星" {
		t.Fatalf("论坛之星候选错误: %+v", got[1])
	}
}

func TestLimitForTargetPriority(t *testing.T) {
	cfg := config.Defaults()
	cfg.Rules = config.PriceRules{N: 4, R: 10, SR: 30, UR: 0}
	cfg.SSRPrices = map[string]int{"欧皇": 200, "氪金大佬": 200}
	cfg.Targets = map[string]config.TargetRule{
		"论坛之星": {Price: 88, Max: 3}, // 定向覆盖 R 类限价 10
		"万人迷":  {Price: 66, Max: 0}, // 只限价不限数量
		"欧皇":   {Price: 5, Max: 1},  // 定向覆盖 SSR 价格 200
	}
	cases := []struct {
		listing market.Listing
		want    int
	}{
		{market.Listing{Name: "论坛之星", Rarity: market.R}, 88},
		{market.Listing{Name: "万人迷", Rarity: market.SR}, 66},
		{market.Listing{Name: "欧皇", Rarity: market.SSR}, 5},
		{market.Listing{Name: "夜猫子", Rarity: market.R}, 10},
		{market.Listing{Name: "潜水员", Rarity: market.N}, 4},
		{market.Listing{Name: "氪金大佬", Rarity: market.SSR}, 200}, // 未定向走 SSR 价格
		{market.Listing{Name: "UR卡", Rarity: market.UR}, 0},
	}
	for _, c := range cases {
		if got := limitFor(&cfg, c.listing); got != c.want {
			t.Fatalf("limitFor(%s/%s)=%d want %d", c.listing.Name, c.listing.Rarity, got, c.want)
		}
	}
}

func TestBuyQtyLimited(t *testing.T) {
	cases := []struct {
		name     string
		remain   int
		maxOwned int
		held     int
		wantQty  int
		wantSkip bool
	}{
		{"不限数量", 5, 0, 99, 5, false},
		{"已满则跳过", 10, 3, 3, 0, true},
		{"超出上限也跳过", 10, 3, 5, 0, true},
		{"补到上限", 10, 3, 1, 2, false},
		{"余量不足", 5, 10, 8, 2, false},
		{"余量为零", 0, 10, 2, 0, false},
	}
	for _, c := range cases {
		qty, skip := buyQtyLimited(c.remain, c.maxOwned, c.held)
		if qty != c.wantQty || skip != c.wantSkip {
			t.Fatalf("%s: buyQtyLimited(%d,%d,%d)=(%d,%v) want (%d,%v)",
				c.name, c.remain, c.maxOwned, c.held, qty, skip, c.wantQty, c.wantSkip)
		}
	}
}

// 回归：addOwned 在 owned 为 nil 时不应 panic（线上曾因此崩溃重启）。
// New() 已初始化 owned；此处再模拟历史异常路径（nil）验证兜底。
func TestAddOwnedNilMapDoesNotPanic(t *testing.T) {
	e := mkEngine(t, config.PriceRules{})
	e.mu.Lock()
	e.owned = nil
	e.mu.Unlock()
	e.addOwned("潜水员", 2)
	if got := e.ownedCount("潜水员"); got != 2 {
		t.Fatalf("addOwned 后持有数应更新: got=%d", got)
	}

	// New() 默认初始化，直接 add 也不应 panic
	e2 := mkEngine(t, config.PriceRules{})
	e2.addOwned("夜猫子", 1)
	if got := e2.ownedCount("夜猫子"); got != 1 {
		t.Fatalf("New 初始化的 owned 应可写: got=%d", got)
	}
}
