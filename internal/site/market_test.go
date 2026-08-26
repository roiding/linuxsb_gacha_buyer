package site

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"gacha-buyer/internal/config"
	"gacha-buyer/internal/market"
)

func TestBuyRequiresConfirmedBalanceChange(t *testing.T) {
	points := 100
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/gacha_market_buy":
			w.Header().Set("Location", "/gacha_market")
			w.WriteHeader(http.StatusSeeOther)
		case "/gacha_market":
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`<article class="gacha-market-card"></article>`))
		case "/gacha_profile":
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("积分 " + itoa(points)))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	cfg := config.Defaults()
	cfg.Site = srv.URL
	c, err := NewClient(&cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	listing := market.Listing{ListingID: 1, Name: "常客", Price: 20, Remain: 2, CSRF: "csrf"}
	res := c.Buy(listing, 2, 100)
	if !res.Submitted || res.OK {
		t.Fatalf("未扣款不应确认成交: %+v", res)
	}

	points = 60
	res = c.Buy(listing, 2, 100)
	if !res.Submitted || !res.OK || res.Qty != 2 || res.Cost != 40 {
		t.Fatalf("扣款后应确认成交: %+v", res)
	}
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	buf := make([]byte, 0, 12)
	for n > 0 {
		buf = append([]byte{byte('0' + n%10)}, buf...)
		n /= 10
	}
	return string(buf)
}

func TestFetchMarketFilteredSendsQuery(t *testing.T) {
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/gacha_market" {
			http.NotFound(w, r)
			return
		}
		gotPath = r.URL.RequestURI()
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`<article class="gacha-market-card"></article>`))
	}))
	defer srv.Close()

	cfg := config.Defaults()
	cfg.Site = srv.URL
	c, _ := NewClient(&cfg, nil)

	if _, err := c.FetchMarketFiltered("sr", "price_asc"); err != nil {
		t.Fatalf("FetchMarketFiltered 失败: %v", err)
	}
	if gotPath != "/gacha_market?rarity=sr&sort=price_asc" {
		t.Fatalf("筛选请求 URL 错误: %q", gotPath)
	}

	if _, err := c.FetchMarket(); err != nil {
		t.Fatalf("FetchMarket 失败: %v", err)
	}
	if gotPath != "/gacha_market" {
		t.Fatalf("默认请求不应带筛选参数: %q", gotPath)
	}
}

// cardHTML 生成一个可解析的市场卡片。
func cardHTML(id int) string {
	return fmt.Sprintf(`<article class="gacha-market-card"><div class="gacha-market-title">
		<span class="gacha-title-badge gacha-title-n"><span class="gacha-title-name">路人甲</span></span></div>
		<div class="gacha-market-meta"><span>单价 <strong>%d</strong> 积分</span><span>剩余 <strong>1</strong> 个</span></div>
		<form class="gacha-market-buy"><input name="_csrf" value="%064d"><input name="listing_id" value="%d"></form>
		</article>`, id*10, id, id)
}

// 分页聚合：满页继续翻；不足一页视为末页停止；跨页按 listing_id 去重。
func TestFetchMarketPaged(t *testing.T) {
	const pageSize = marketPageSize
	fullPage := make([]string, 0, pageSize)
	for i := 1; i <= pageSize; i++ {
		fullPage = append(fullPage, cardHTML(i))
	}
	dupPage := []string{cardHTML(pageSize), cardHTML(801)} // 首条与上页末条重复

	var requests []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/gacha_market" {
			http.NotFound(w, r)
			return
		}
		requests = append(requests, r.URL.Query().Get("p"))
		page := fullPage
		if r.URL.Query().Get("p") == "2" {
			page = dupPage
		}
		fmt.Fprint(w, strings.Join(page, "\n"))
	}))
	defer srv.Close()

	cfg := config.Defaults()
	cfg.Site = srv.URL
	c, _ := NewClient(&cfg, nil)

	listings, err := c.FetchMarketPaged(MarketQuery{}, nil)
	if err != nil {
		t.Fatalf("分页抓取失败: %v", err)
	}
	if len(requests) != 2 {
		t.Fatalf("第 2 页不足一页应停止翻页，实际请求 p 序列: %v", requests)
	}
	if len(listings) != pageSize+1 {
		t.Fatalf("去重后应 %d 条，得到 %d", pageSize+1, len(listings))
	}
	found := false
	for _, l := range listings {
		if l.ListingID == 801 {
			found = true
		}
	}
	if !found {
		t.Fatal("第 2 页新增条目未被抓到")
	}
}

// visit 回调返回 true 时提前终止翻页。
func TestFetchMarketPagedEarlyStop(t *testing.T) {
	const pageSize = marketPageSize
	fullPage := make([]string, 0, pageSize)
	for i := 1; i <= pageSize; i++ {
		fullPage = append(fullPage, cardHTML(i))
	}
	var count int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		count++
		fmt.Fprint(w, strings.Join(fullPage, "\n"))
	}))
	defer srv.Close()

	cfg := config.Defaults()
	cfg.Site = srv.URL
	c, _ := NewClient(&cfg, nil)

	listings, err := c.FetchMarketPaged(MarketQuery{}, func([]market.Listing) bool { return true })
	if err != nil {
		t.Fatalf("分页抓取失败: %v", err)
	}
	if count != 1 || len(listings) != pageSize {
		t.Fatalf("visit 提前终止应只请求 1 页: count=%d listings=%d", count, len(listings))
	}
}

// 翻页请求应带上 q/rarity/sort/p 参数。
func TestFetchMarketPagedSendsParams(t *testing.T) {
	var uris []string
	stop := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		uris = append(uris, r.URL.RequestURI())
		q := r.URL.Query()
		page := 1
		if q.Get("p") != "" {
			fmt.Sscanf(q.Get("p"), "%d", &page)
		}
		if page >= 2 {
			close(stop)
			fmt.Fprint(w, `<div class="gacha-market-grid"></div>`)
			return
		}
		body := make([]string, 0, marketPageSize)
		for i := 1; i <= marketPageSize; i++ {
			body = append(body, cardHTML(i))
		}
		fmt.Fprint(w, strings.Join(body, "\n"))
	}))
	defer srv.Close()

	cfg := config.Defaults()
	cfg.Site = srv.URL
	c, _ := NewClient(&cfg, nil)

	_, err := c.FetchMarketPaged(MarketQuery{Q: "路人甲", Rarity: "n", Sort: "price_asc"}, nil)
	if err != nil {
		t.Fatalf("分页抓取失败: %v", err)
	}
	if len(uris) < 2 || uris[0] != "/gacha_market?q=%E8%B7%AF%E4%BA%BA%E7%94%B2&rarity=n&sort=price_asc" ||
		uris[1] != "/gacha_market?p=2&q=%E8%B7%AF%E4%BA%BA%E7%94%B2&rarity=n&sort=price_asc" {
		t.Fatalf("翻页应保留筛选参数并递增 p: %v", uris)
	}
}

func TestParsePublishOption(t *testing.T) {
	name, n := parsePublishOption("🍀 欧皇（可出售 1）")
	if name != "欧皇" || n != 1 {
		t.Fatalf("解析上架选项错误: %q %d", name, n)
	}
	name, n = parsePublishOption("🪶 打酱油的（可出售 2）")
	if name != "打酱油的" || n != 2 {
		t.Fatalf("解析多数量选项错误: %q %d", name, n)
	}
	name, n = parsePublishOption("👩🏻‍🦰 万人迷（可出售 3）") // 多 rune emoji（ZWJ）
	if name != "万人迷" || n != 3 {
		t.Fatalf("解析 ZWJ emoji 选项错误: %q %d", name, n)
	}
}

func TestFetchPublishableTitles(t *testing.T) {
	page := `<form method="post" action="/gacha_market_publish">
		<input type="hidden" name="_csrf" value="cafe001">
		<select name="title_id">
			<option value="16">🍀 欧皇（可出售 1）</option>
			<option value="5">🪶 打酱油的（可出售 2）</option>
		</select>
		<input name="quantity" type="number" value="1">
		<input name="unit_price" type="number" value="10">
	</form>`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/gacha_market" {
			fmt.Fprint(w, page)
			return
		}
		http.NotFound(w, r)
	}))
	defer srv.Close()

	cfg := config.Defaults()
	cfg.Site = srv.URL
	c, _ := NewClient(&cfg, nil)
	titles, err := c.FetchPublishableTitles()
	if err != nil {
		t.Fatalf("FetchPublishableTitles 失败: %v", err)
	}
	if len(titles) != 2 {
		t.Fatalf("应解析 2 个可出售称号: %+v", titles)
	}
	if titles[0].TitleID != 16 || titles[0].Name != "欧皇" || titles[0].Sellable != 1 {
		t.Fatalf("欧皇解析错误: %+v", titles[0])
	}
	if titles[1].Name != "打酱油的" || titles[1].Sellable != 2 {
		t.Fatalf("打酱油的解析错误: %+v", titles[1])
	}
}

func TestPublishTitleAndCancelListing(t *testing.T) {
	var publishForm, cancelForm url.Values
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/gacha_market" && r.Method == http.MethodGet {
			fmt.Fprint(w, `<form method="post" action="/gacha_market_publish"><input name="_csrf" value="0123456789abcdef"></form>
				<form method="post" action="/gacha_market_cancel"><input name="_csrf" value="0123456789abcdef"></form>`)
			return
		}
		if r.URL.Path == "/gacha_market_publish" && r.Method == http.MethodPost {
			_ = r.ParseForm()
			publishForm = r.PostForm
			fmt.Fprint(w, `<div class="alert alert-success">发布成功</div>`)
			return
		}
		if r.URL.Path == "/gacha_market_cancel" && r.Method == http.MethodPost {
			_ = r.ParseForm()
			cancelForm = r.PostForm
			fmt.Fprint(w, `<div class="alert alert-success">已撤回</div>`)
			return
		}
		http.NotFound(w, r)
	}))
	defer srv.Close()

	cfg := config.Defaults()
	cfg.Site = srv.URL
	c, _ := NewClient(&cfg, nil)

	res := c.PublishTitle(16, 2, 66, 24)
	if !res.OK || res.TitleID != 16 {
		t.Fatalf("上架结果错误: %+v", res)
	}
	if publishForm.Get("title_id") != "16" || publishForm.Get("quantity") != "2" ||
		publishForm.Get("unit_price") != "66" || publishForm.Get("duration_hours") != "24" {
		t.Fatalf("上架请求体错误: %v", publishForm)
	}

	cres := c.CancelListing(868)
	if !cres.OK || cres.ListingID != 868 {
		t.Fatalf("下架结果错误: %+v", cres)
	}
	if cancelForm.Get("listing_id") != "868" {
		t.Fatalf("下架请求体错误: %v", cancelForm)
	}
}

func TestMyListingIDs(t *testing.T) {
	page := `<form method="post" action="/gacha_market_cancel"><input name="_csrf" value="x"><input name="listing_id" value="868"></form>
		<form method="post" action="/gacha_market_cancel"><input name="_csrf" value="x"><input name="listing_id" value="867"></form>
		<form method="post" action="/gacha_market_cancel"><input name="_csrf" value="x"><input name="listing_id" value="868"></form>`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/gacha_market" {
			fmt.Fprint(w, page)
			return
		}
		http.NotFound(w, r)
	}))
	defer srv.Close()

	cfg := config.Defaults()
	cfg.Site = srv.URL
	c, _ := NewClient(&cfg, nil)
	ids, err := c.MyListingIDs()
	if err != nil {
		t.Fatalf("MyListingIDs 失败: %v", err)
	}
	if len(ids) != 2 || ids[0] != 868 || ids[1] != 867 {
		t.Fatalf("我的挂牌 id 解析错误（应去重）: %v", ids)
	}
}
