package buyer

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
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

// mktCard 生成一个可被 market.ParseMarket 解析的在售卡片。
func mktCard(id int, name, rarity string, price int) string {
	return fmt.Sprintf(`<article class="gacha-market-card"><div class="gacha-market-title">
		<span class="gacha-title-badge gacha-title-%s"><span class="gacha-title-name">%s</span></span></div>
		<div class="gacha-market-meta"><span>单价 <strong>%d</strong> 积分</span><span>剩余 <strong>1</strong> 个</span></div>
		<form class="gacha-market-buy"><input name="_csrf" value="cs"><input name="listing_id" value="%d"></form>
		</article>`, rarity, name, price, id)
}

func mktPage(cards []string) string { return strings.Join(cards, "\n") }

// 价格升序扫描的翻页止损：页内出现超过分类最大限价的挂牌即停止翻页（升序下
// 后续只可能更贵）；整页都在限价内才继续。maxLimit<=0 时不发请求。
func TestFetchCategoryAscEarlyStop(t *testing.T) {
	var hits int
	page := func(prices []int) string {
		cards := make([]string, 0, len(prices))
		for i, p := range prices {
			cards = append(cards, mktCard(i+1, "路人甲", "n", p))
		}
		return mktPage(cards)
	}
	newSrv := func(t *testing.T, pages map[string][]int) *httptest.Server {
		t.Helper()
		return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != "/gacha_market" {
				http.NotFound(w, r)
				return
			}
			hits++
			p := r.URL.Query().Get("p")
			if p == "" {
				p = "1"
			}
			fmt.Fprint(w, page(pages[p]))
		}))
	}

	cfg := config.Defaults()
	cfg.Rules = config.PriceRules{N: 4}
	mkClient := func(url string) *site.Client {
		cfg.Site = url // client 在创建时锁定站点，须先设 Site 再建
		cl, _ := site.NewClient(&cfg, nil)
		return cl
	}

	// 场景1：第一页全部 10 分（>4），只请求第一页
	hits = 0
	all10 := make([]int, 24)
	for i := range all10 {
		all10[i] = 10
	}
	srv := newSrv(t, map[string][]int{"1": all10})
	listings, err := fetchCategoryAsc(mkClient(srv.URL), market.N, 4)
	if err != nil {
		t.Fatal(err)
	}
	if hits != 1 || len(listings) != 24 {
		t.Fatalf("整页超限应只请求第一页: hits=%d listings=%d", hits, len(listings))
	}
	srv.Close()

	// 场景2：第一页混排（3 分限价内 + 10 分超限）——只要出现超限即停，不翻第二页，
	// 且限价内的 3 分挂牌已在结果中
	hits = 0
	mixed := make([]int, 24)
	mixed[0] = 3
	copy(mixed[1:], all10)
	srv = newSrv(t, map[string][]int{"1": mixed, "2": all10})
	listings, err = fetchCategoryAsc(mkClient(srv.URL), market.N, 4)
	if err != nil {
		t.Fatal(err)
	}
	if hits != 1 || len(listings) != 24 {
		t.Fatalf("页内出现超限挂牌应立即停止翻页: hits=%d listings=%d", hits, len(listings))
	}
	if listings[0].Price != 3 {
		t.Fatalf("限价内挂牌应被抓到: %+v", listings[0])
	}
	srv.Close()

	// 场景3：maxLimit<=0（分类无任何限价规则）不发起请求
	hits = 0
	srv = newSrv(t, map[string][]int{"1": all10})
	listings, err = fetchCategoryAsc(mkClient(srv.URL), market.N, 0)
	if err != nil || len(listings) != 0 || hits != 0 {
		t.Fatalf("无限价规则应零请求: err=%v listings=%d hits=%d", err, len(listings), hits)
	}
	srv.Close()
}

// SSR 按名称定价时不能按"单条超自身限价"止损：第一页全是未配价称号（逐条看都超限），
// 但只要页内价格未超过分类最大限价（欧皇 200）就继续翻页，直到抓到第二页的欧皇。
func TestFetchCategoryAscPerNamePaging(t *testing.T) {
	var hits int
	cheap := make([]int, 24) // 24 张 150 分的未配价 SSR
	for i := range cheap {
		cheap[i] = 150
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/gacha_market" {
			http.NotFound(w, r)
			return
		}
		hits++
		if p := r.URL.Query().Get("p"); p == "2" {
			fmt.Fprint(w, mktPage([]string{mktCard(90, "欧皇", "ssr", 180)}))
			return
		}
		cards := make([]string, 0, 24)
		for i := 1; i <= 24; i++ {
			cards = append(cards, mktCard(i, "路人SSR", "ssr", 150))
		}
		fmt.Fprint(w, mktPage(cards))
	}))
	defer srv.Close()

	cfg := config.Defaults()
	cfg.SSRPrices = map[string]int{"欧皇": 200}
	cfg.Site = srv.URL
	c, _ := site.NewClient(&cfg, nil)

	listings, err := fetchCategoryAsc(c, market.SSR, 200)
	if err != nil {
		t.Fatal(err)
	}
	if hits != 2 {
		t.Fatalf("第一页未超分类最大限价应继续翻页: hits=%d", hits)
	}
	found := false
	for _, l := range listings {
		if l.Name == "欧皇" && l.Price == 180 {
			found = true
		}
	}
	if !found {
		t.Fatal("第二页的欧皇应被抓到")
	}
}

// categoryMaxLimit：类型限价、SSR 单卡价、目录缓存归类的定向价取最大值。
func TestCategoryMaxLimit(t *testing.T) {
	e := mkEngine(t, config.PriceRules{N: 4, SR: 30})
	cfg := config.Defaults()
	cfg.Rules = config.PriceRules{N: 4, SR: 30}
	cfg.SSRPrices = map[string]int{"欧皇": 200, "氪金大佬": 260}
	cfg.Targets = map[string]config.TargetRule{
		"话题王": {Price: 50}, // R，经目录缓存归类
		"UR卡": {Price: 500},
	}
	e.cfg = &cfg
	e.mu.Lock()
	e.titleRarity = map[string]market.Rarity{"话题王": market.R, "UR卡": market.UR}
	e.mu.Unlock()

	cases := []struct {
		r    market.Rarity
		want int
	}{
		{market.N, 4},
		{market.R, 50}, // 定向价 50
		{market.SR, 30},
		{market.SSR, 260}, // SSR 单卡最大价
		{market.UR, 500},  // 定向价 500
	}
	for _, c := range cases {
		if got := e.categoryMaxLimit(&cfg, c.r); got != c.want {
			t.Fatalf("%s 最大限价错误: got=%d want=%d", c.r, got, c.want)
		}
	}

	// 目录缓存为空时定向价不归属任何分类（不影响类型限价）
	e.mu.Lock()
	e.titleRarity = nil
	e.mu.Unlock()
	if got := e.categoryMaxLimit(&cfg, market.R); got != 0 {
		t.Fatalf("无目录缓存时 R 应只剩类型限价 0: got=%d", got)
	}
}

// fetchBuyables：请求数 = 启用分类数，各分类结果按 listing_id 去重合并；
// 未启用分类时不发任何请求。
func TestFetchBuyablesPerCategoryRequests(t *testing.T) {
	var hits []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/gacha_market" {
			http.NotFound(w, r)
			return
		}
		q := r.URL.Query()
		p := q.Get("p")
		if p == "" {
			p = "1"
		}
		hits = append(hits, q.Get("rarity")+"|p="+p)
		cards := make([]string, 0, 5)
		if q.Get("rarity") == "n" {
			cards = append(cards, mktCard(1, "路人甲", "n", 3))
		} else {
			cards = append(cards, mktCard(2, "夜猫子", "r", 5))
		}
		fmt.Fprint(w, mktPage(cards))
	}))
	defer srv.Close()

	cfg := config.Defaults()
	cfg.Rules = config.PriceRules{N: 4, R: 10}
	cfg.Site = srv.URL
	c, _ := site.NewClient(&cfg, nil)
	e := mkEngine(t, cfg.Rules)
	e.cfg = &cfg

	listings, err := e.fetchBuyables(c, &cfg)
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 2 {
		t.Fatalf("应恰好请求 N、R 各一次: %v", hits)
	}
	if len(listings) != 2 || listings[0].Name != "路人甲" || listings[1].Name != "夜猫子" {
		t.Fatalf("分类合并结果错误: %+v", listings)
	}

	// 未启用任何分类：0 请求
	hits = nil
	cfg.Rules = config.PriceRules{}
	cfg.SSRPrices = map[string]int{}
	listings, err = e.fetchBuyables(c, &cfg)
	if err != nil || len(listings) != 0 || len(hits) != 0 {
		t.Fatalf("无启用分类应零请求: err=%v listings=%d hits=%v", err, len(listings), hits)
	}
}

// 称号目录缓存：定向名全部命中后第二次调用不再请求目录页。
func TestRaritiesWithTargetsCachesCatalog(t *testing.T) {
	var catalogHits int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/gacha" {
			catalogHits++
			fmt.Fprint(w, `<div class="gacha-all-item"><span class="gacha-title-badge gacha-title-r">
				<span class="gacha-title-name">话题王</span></span></div>`)
			return
		}
		http.NotFound(w, r)
	}))
	defer srv.Close()

	cfg := config.Defaults()
	cfg.Rules = config.PriceRules{N: 4}
	cfg.Targets = map[string]config.TargetRule{"话题王": {Price: 10}}
	cfg.Site = srv.URL
	c, _ := site.NewClient(&cfg, nil)
	e := mkEngine(t, cfg.Rules)
	e.cfg = &cfg

	for i := 0; i < 3; i++ {
		got := e.raritiesWithTargets(c, activeRarities(&cfg), &cfg)
		if len(got) != 2 || got[0] != market.N || got[1] != market.R {
			t.Fatalf("第 %d 次分类推算错误: %v", i+1, got)
		}
	}
	if catalogHits != 1 {
		t.Fatalf("目录应只请求一次（缓存命中）: %d", catalogHits)
	}

	// 新增未缓存的定向名 → 重新拉目录
	cfg.Targets["新称号"] = config.TargetRule{Price: 5}
	_ = e.raritiesWithTargets(c, activeRarities(&cfg), &cfg)
	if catalogHits != 2 {
		t.Fatalf("出现未命中定向名应重新拉目录: %d", catalogHits)
	}
}
