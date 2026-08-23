package buyer

import (
	"path/filepath"
	"testing"

	"gacha-buyer/internal/config"
	"gacha-buyer/internal/db"
	"gacha-buyer/internal/market"
	"gacha-buyer/internal/store"
)

func mkEngine(t *testing.T, rules config.PriceRules, maxSpend int, spent int) *Engine {
	t.Helper()
	cfg := config.Defaults()
	cfg.Rules = rules
	cfg.MaxSpend = maxSpend
	d, err := db.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	st := store.New(d)
	if spent > 0 {
		_ = st.Add(store.Purchase{OK: true, Cost: spent, Qty: 1, Price: 1})
	}
	return New(&cfg, st, nil)
}

func TestMatchRulesAndBudget(t *testing.T) {
	e := mkEngine(t, config.PriceRules{SR: 30, R: 10, N: 4}, 500, 0)
	all := []market.Listing{
		{ListingID: 1, Name: "万人迷", Rarity: market.SR, Price: 30, Remain: 3, CSRF: "x"},
		{ListingID: 2, Name: "欧皇", Rarity: market.SSR, Price: 200, Remain: 5, CSRF: "x"}, // SSR 不收
		{ListingID: 3, Name: "夜猫子", Rarity: market.R, Price: 11, Remain: 10, CSRF: "x"},  // 超限价
		{ListingID: 4, Name: "夜猫子", Rarity: market.R, Price: 10, Remain: 30, CSRF: "x"},  // 命中
		{ListingID: 5, Name: "潜水员", Rarity: market.N, Price: 4, Remain: 1, CSRF: "x"},    // 命中
	}
	got := e.match(all)
	if len(got) != 3 {
		t.Fatalf("期望命中 3 条，得到 %d: %+v", len(got), got)
	}
	// listing 1：剩 3 全买（≤MaxBuyOnce=5）
	if got[0].Remain != 3 {
		t.Errorf("listing1 数量=%d，want 3", got[0].Remain)
	}
	// listing 4：30 个只买 MaxBuyOnce=5 个
	if got[1].ListingID != 4 || got[1].Remain != 5 {
		t.Errorf("listing4 数量=%d，want 5", got[1].Remain)
	}
}

func TestMatchBudgetCap(t *testing.T) {
	e := mkEngine(t, config.PriceRules{R: 10}, 25, 0)
	all := []market.Listing{
		{ListingID: 9, Name: "夜猫子", Rarity: market.R, Price: 10, Remain: 30, CSRF: "x"},
	}
	got := e.match(all)
	// 预算 25 只够买 2 个
	if len(got) != 1 || got[0].Remain != 2 {
		t.Fatalf("预算裁剪错误: %+v", got)
	}
}

func TestMatchSpentExhaustsBudget(t *testing.T) {
	e := mkEngine(t, config.PriceRules{N: 4}, 100, 100)
	all := []market.Listing{{ListingID: 5, Name: "潜水员", Rarity: market.N, Price: 4, Remain: 1}}
	if got := e.match(all); len(got) != 0 {
		t.Fatalf("预算耗尽应不买: %+v", got)
	}
}

func TestMatchZeroLimitSkips(t *testing.T) {
	e := mkEngine(t, config.PriceRules{SR: 30}, 500, 0) // R/N 未设=不收
	all := []market.Listing{
		{ListingID: 6, Name: "夜猫子", Rarity: market.R, Price: 5, Remain: 9},
		{ListingID: 7, Name: "潜水员", Rarity: market.N, Price: 2, Remain: 9},
	}
	if got := e.match(all); len(got) != 0 {
		t.Fatalf("未配置稀有度不应买: %+v", got)
	}
}

func TestMatchMaxListings(t *testing.T) {
	e := mkEngine(t, config.PriceRules{N: 4}, 500, 0)
	e.cfg.MaxListings = 2
	var all []market.Listing
	for i := 1; i <= 5; i++ {
		all = append(all, market.Listing{ListingID: i, Name: "潜", Rarity: market.N, Price: 2, Remain: 1, CSRF: "x"})
	}
	if got := e.match(all); len(got) != 2 {
		t.Fatalf("每轮上限 2 条，得到 %d", len(got))
	}
}
