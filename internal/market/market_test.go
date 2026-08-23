package market

import (
	"testing"
)

// 与线上新版结构一致的卡片样本（徽章为 icon/name/rarity 三段式，含 ZWJ emoji）。
const fixturePage = `<!doctype html><html><body><main>
<article class="gacha-market-card"><div class="gacha-market-title"><span class="gacha-title-badge gacha-title-ssr" style="--gacha-color:var(--warning)"><span class="gacha-title-icon">💎</span><span class="gacha-title-name">氪金大佬</span><span class="gacha-title-rarity">SSR</span></span></div><div class="gacha-market-meta"><span>单价 <strong>300</strong> 积分</span><span>剩余 <strong>1</strong> 个</span><span>剩余时间 <strong>3 天</strong></span></div><form class="gacha-market-buy" method="post" action="/gacha_market_buy"><input type="hidden" name="_csrf" value="a1b2c3d4a1b2c3d4a1b2c3d4a1b2c3d4a1b2c3d4a1b2c3d4a1b2c3d4a1b2c3d4"><input type="hidden" name="listing_id" value="21"><input type="number" name="quantity" min="1" max="1" value="1"><button type="submit">购买</button></form></article>
<article class="gacha-market-card"><div class="gacha-market-title"><span class="gacha-title-badge gacha-title-sr"><span class="gacha-title-icon">👩🏻‍🦰</span><span class="gacha-title-name">万人迷</span><span class="gacha-title-rarity">SR</span></span></div><div class="gacha-market-meta"><span>单价 <strong>50</strong> 积分</span><span>剩余 <strong>3</strong> 个</span><span>剩余时间 <strong>7 天</strong></span></div><form class="gacha-market-buy" method="post" action="/gacha_market_buy"><input type="hidden" name="_csrf" value="a1b2c3d4a1b2c3d4a1b2c3d4a1b2c3d4a1b2c3d4a1b2c3d4a1b2c3d4a1b2c3d4"><input type="hidden" name="listing_id" value="20"><input type="number" name="quantity" min="1" value="1"><button type="submit">购买</button></form></article>
<article class="gacha-market-card"><div class="gacha-market-title"><span class="gacha-title-badge gacha-title-r"><span class="gacha-title-icon">🦉</span><span class="gacha-title-name">夜猫子</span><span class="gacha-title-rarity">R</span></span></div><div class="gacha-market-meta"><span>单价 <strong>20</strong> 积分</span><span>剩余 <strong>30</strong> 个</span><span>剩余时间 <strong>23 小时</strong></span></div><form class="gacha-market-buy" method="post" action="/gacha_market_buy"><input type="hidden" name="_csrf" value="a1b2c3d4a1b2c3d4a1b2c3d4a1b2c3d4a1b2c3d4a1b2c3d4a1b2c3d4a1b2c3d4"><input type="hidden" name="listing_id" value="1"><input type="number" name="quantity" min="1" value="1"><button type="submit">购买</button></form></article>
<article class="gacha-market-card"><div class="gacha-market-title"><span class="gacha-title-badge gacha-title-n"><span class="gacha-title-icon">🚶</span><span class="gacha-title-name">路人甲</span><span class="gacha-title-rarity">N</span></span></div><div class="gacha-market-meta"><span>单价 <strong>15</strong> 积分</span><span>剩余 <strong>1</strong> 个</span><span>剩余时间 <strong>24 小时</strong></span></div><form class="gacha-market-buy" method="post" action="/gacha_market_buy"><input type="hidden" name="_csrf" value="a1b2c3d4a1b2c3d4a1b2c3d4a1b2c3d4a1b2c3d4a1b2c3d4a1b2c3d4a1b2c3d4"><input type="hidden" name="listing_id" value="77"><input type="number" name="quantity" min="1" max="1" value="1"><button type="submit">购买</button></form></article>
</main></body></html>`

func TestParseMarket(t *testing.T) {
	listings, _, err := ParseMarket(fixturePage)
	if err != nil {
		t.Fatalf("ParseMarket: %v", err)
	}
	if len(listings) != 4 {
		t.Fatalf("期望 4 条，得到 %d", len(listings))
	}
	want := []struct {
		id     int
		rarity Rarity
		name   string
		emoji  string
		price  int
		remain int
	}{
		{21, SSR, "氪金大佬", "💎", 300, 1},
		{20, SR, "万人迷", "👩🏻‍🦰", 50, 3},
		{1, R, "夜猫子", "🦉", 20, 30},
		{77, N, "路人甲", "🚶", 15, 1},
	}
	for i, w := range want {
		l := listings[i]
		if l.ListingID != w.id || l.Rarity != w.rarity || l.Name != w.name ||
			l.Emoji != w.emoji || l.Price != w.price || l.Remain != w.remain {
			t.Errorf("第 %d 条不符: got %+v want %+v", i, l, w)
		}
		if len(l.CSRF) != 64 {
			t.Errorf("第 %d 条 CSRF 缺失", i)
		}
	}
}

func TestParseMarketEmpty(t *testing.T) {
	if _, _, err := ParseMarket("<html><main>空空如也</main></html>"); err == nil {
		t.Fatal("无卡片页面应报错")
	}
}
