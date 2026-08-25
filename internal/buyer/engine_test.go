package buyer

import (
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
