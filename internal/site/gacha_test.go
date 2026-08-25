package site

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"gacha-buyer/internal/config"
)

func TestParseGachaResultWin(t *testing.T) {
	page := `<div class="gacha-result-card gacha-result-sr">
		<div class="gacha-result-rarity">SR</div><div class="gacha-result-icon">👻</div>
		<div class="gacha-result-name">键盘侠</div><div class="gacha-result-new">NEW!</div>
		<div class="gacha-result-desc">吐槽力度拉满</div><div class="gacha-result-quantity">获得 × 1</div>
	</div>`
	res := parseGachaResult(page)
	if !res.OK || !res.Drawn || res.Title != "键盘侠" {
		t.Fatalf("应解析出称号: %+v", res)
	}
}

func TestParseGachaResultEmpty(t *testing.T) {
	page := `<div class="gacha-result-card"><div class="gacha-result-icon">💨</div>很遗憾 什么都没抽到，下次再来！</div>`
	res := parseGachaResult(page)
	if !res.OK || !res.Drawn || res.Title != "" {
		t.Fatalf("空包应 OK 且无称号: %+v", res)
	}
}

func TestParseGachaResultUnrecognizedStillOK(t *testing.T) {
	// POST 已消费但结构无法识别：绝不重复抽。
	res := parseGachaResult(`<html><body>某些未知页面</body></html>`)
	if !res.OK || !res.Drawn || res.Title != "" {
		t.Fatalf("未知结果也应视为已抽: %+v", res)
	}
}

func TestParseGachaResultLoginPage(t *testing.T) {
	res := parseGachaResult(`<form action="/login"><input name="password"></form>`)
	if res.OK || res.Drawn {
		t.Fatalf("会话失效不应视为已抽: %+v", res)
	}
}

func TestFindFreePullCSRF(t *testing.T) {
	page := `<form method="post" action="/search">...</form>
		<form method="post" action="/gacha_pull"><input type="hidden" name="_csrf" value="abc123def456"><button>今日免费一抽</button></form>
		<form method="post" action="/gacha_pull_10"><input type="hidden" name="_csrf" value="ten"><button>十连抽 (90 积分)</button></form>`
	csrf, ok := findFreePullCSRF(page)
	if !ok || csrf != "abc123def456" {
		t.Fatalf("应找到免费抽表单的 csrf: %q %v", csrf, ok)
	}

	// 今日已抽：只剩付费单抽与十连，不应找到。
	after := `<form method="post" action="/gacha_pull"><input type="hidden" name="_csrf" value="pay"><button>抽一次 (10 积分)</button></form>`
	if _, ok := findFreePullCSRF(after); ok {
		t.Fatal("付费单抽不应被当作免费抽")
	}
}

func TestParseProfileTitles(t *testing.T) {
	page := `<div class="gacha-profile-item is-equipped">
		<span class="gacha-title-name">潜水员</span>
		<span class="gacha-status-tag gacha-status-permanent">× 1</span>
		<span class="gacha-equipped-label">已装备</span>
		<button class="gacha-gift-btn" onclick="gachaGiftModalOpen({&quot;id&quot;:2,&quot;name&quot;:&quot;\u6f5c\u6c34\u5458&quot;})">赠送</button>
	</div><div class="gacha-profile-item">
		<span class="gacha-title-name">键盘侠</span>
		<span class="gacha-status-tag gacha-status-permanent">× 1</span>
		<button class="gacha-gift-btn" onclick="gachaGiftModalOpen({&quot;id&quot;:13,&quot;name&quot;:&quot;\u952e\u76d8\u4fa0&quot;})">赠送</button>
	</div><div class="gacha-profile-item">
		<span class="gacha-title-name">常客</span>
		<span class="gacha-status-tag gacha-status-permanent">× 2</span>
		<button class="gacha-gift-btn" onclick="gachaGiftModalOpen({&quot;id&quot;:6,&quot;name&quot;:&quot;\u5e38\u5ba2&quot;})">赠送</button>
	</div>`
	titles := parseProfileTitles(page)
	if len(titles) != 3 {
		t.Fatalf("应解析 3 个称号: %+v", titles)
	}
	byName := map[string]ProfileTitle{}
	for _, x := range titles {
		byName[x.Name] = x
	}
	if x := byName["潜水员"]; x.ID != 2 || x.Count != 1 || !x.Equipped {
		t.Fatalf("潜水员应 id=2 ×1 已装备: %+v", x)
	}
	if x := byName["键盘侠"]; x.ID != 13 || x.Count != 1 || x.Equipped {
		t.Fatalf("键盘侠应 id=13 ×1 未装备: %+v", x)
	}
	if x := byName["常客"]; x.ID != 6 || x.Count != 2 || x.Equipped {
		t.Fatalf("常客应 id=6 ×2 未装备: %+v", x)
	}
}

func TestFindGiftableTitleDeductsEquipped(t *testing.T) {
	titles := []ProfileTitle{
		{ID: 2, Name: "潜水员", Count: 1, Equipped: true},
		{ID: 13, Name: "键盘侠", Count: 1, Equipped: false},
		{ID: 6, Name: "常客", Count: 2, Equipped: false},
	}
	if id, n, ok := findGiftableTitle(titles, "潜水员"); !ok || n != 0 {
		t.Fatalf("仅 1 张且佩戴中应不可赠送: id=%d n=%d ok=%v", id, n, ok)
	}
	if id, n, ok := findGiftableTitle(titles, "键盘侠"); !ok || id != 13 || n != 1 {
		t.Fatalf("键盘侠应可赠 1 张: id=%d n=%d ok=%v", id, n, ok)
	}
	if id, n, ok := findGiftableTitle(titles, "常客"); !ok || id != 6 || n != 2 {
		t.Fatalf("常客应可赠 2 张: id=%d n=%d ok=%v", id, n, ok)
	}
	if _, _, ok := findGiftableTitle(titles, "不存在"); ok {
		t.Fatal("不存在的称号不应匹配")
	}
}

func TestUsernameFromProfilePage(t *testing.T) {
	page := `<html><head><title>neo怕不怕进去 - 烧饼社区 - 人人都有饼吃的AI社区！</title></head></html>`
	if got := UsernameFromProfilePage(page); got != "neo怕不怕进去" {
		t.Fatalf("用户名解析错误: %q", got)
	}
}

const drawPageHTML = `<form method="post" action="/search">...</form>
	<form method="post" action="/gacha_pull"><input type="hidden" name="_csrf" value="cafebabe1234567890abcdef1234567890"><button>今日免费一抽</button></form>`

const winResultPage = `<html><body><div class="gacha-result-card gacha-result-sr">
	<div class="gacha-result-rarity">SR</div><div class="gacha-result-icon">👻</div>
	<div class="gacha-result-name">键盘侠</div><div class="gacha-result-new">NEW!</div>
	</div></body></html>`

func TestDrawGachaFlow(t *testing.T) {
	var postForm url.Values
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/gacha":
			fmt.Fprint(w, drawPageHTML)
		case r.Method == http.MethodPost && r.URL.Path == "/gacha_pull":
			_ = r.ParseForm()
			postForm = r.PostForm
			w.Header().Set("Location", "/gacha_pull?result=abc")
			w.WriteHeader(http.StatusFound)
		case r.Method == http.MethodGet && r.URL.Path == "/gacha_pull":
			fmt.Fprint(w, winResultPage)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	cfg := config.Defaults()
	cfg.Site = server.URL
	client, err := NewClient(&cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	res := client.DrawGacha()
	if !res.OK || !res.Drawn || res.Title != "键盘侠" {
		t.Fatalf("抽卡流程结果错误: %+v", res)
	}
	if postForm.Get("_csrf") != "cafebabe1234567890abcdef1234567890" {
		t.Fatalf("提交的 csrf 错误: %v", postForm)
	}
}

func TestDrawGachaAlreadyDrawnNoPost(t *testing.T) {
	postCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/gacha":
			fmt.Fprint(w, `<form method="post" action="/gacha_pull"><input name="_csrf" value="pay"><button>抽一次 (10 积分)</button></form>`)
		case r.Method == http.MethodPost:
			postCount++
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	cfg := config.Defaults()
	cfg.Site = server.URL
	client, _ := NewClient(&cfg, nil)
	res := client.DrawGacha()
	if !res.OK || res.Drawn || postCount != 0 {
		t.Fatalf("今日已抽应直接返回且不 POST: %+v posts=%d", res, postCount)
	}
}

func TestGiftTitleFlow(t *testing.T) {
	const profilePage = `<form method="post" action="/search">...</form>
		<div class="gacha-profile-item">
			<span class="gacha-title-name">键盘侠</span>
			<span class="gacha-status-tag gacha-status-permanent">× 1</span>
			<button class="gacha-gift-btn" onclick="gachaGiftModalOpen({&quot;id&quot;:13,&quot;name&quot;:&quot;\u952e\u76d8\u4fa0&quot;})">赠送</button>
		</div>
		<form method="post" action="/gacha_gift"><input type="hidden" name="_csrf" value="0123456789abcdef0123456789abcdef"></form>`
	var postForm url.Values
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/gacha_profile":
			fmt.Fprint(w, profilePage)
		case r.Method == http.MethodPost && r.URL.Path == "/gacha_gift":
			_ = r.ParseForm()
			postForm = r.PostForm
			fmt.Fprint(w, `<div class="alert alert-success">已赠送给 Se7en</div>`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	cfg := config.Defaults()
	cfg.Site = server.URL
	client, _ := NewClient(&cfg, nil)
	res := client.GiftTitle("Se7en", "键盘侠")
	if !res.OK || !res.Gifted || res.Target != "Se7en" || res.TitleID != 13 {
		t.Fatalf("赠送流程结果错误: %+v", res)
	}
	if postForm.Get("title_id") != "13" || postForm.Get("username") != "Se7en" || postForm.Get("_csrf") != "0123456789abcdef0123456789abcdef" {
		t.Fatalf("赠送请求体错误: %v", postForm)
	}
}

func TestGiftTitleEquippedOnlyRefuses(t *testing.T) {
	const profilePage = `<div class="gacha-profile-item is-equipped">
			<span class="gacha-title-name">潜水员</span>
			<span class="gacha-status-tag gacha-status-permanent">× 1</span>
			<span class="gacha-equipped-label">已装备</span>
			<button class="gacha-gift-btn" onclick="gachaGiftModalOpen({&quot;id&quot;:2,&quot;name&quot;:&quot;\u6f5c\u6c34\u5458&quot;})">赠送</button>
		</div>`
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, profilePage)
	}))
	defer server.Close()

	cfg := config.Defaults()
	cfg.Site = server.URL
	client, _ := NewClient(&cfg, nil)
	res := client.GiftTitle("Se7en", "潜水员")
	if res.OK || res.Gifted {
		t.Fatalf("仅 1 张且佩戴中应拒绝赠送: %+v", res)
	}
	if !strings.Contains(res.Message, "佩戴") {
		t.Fatalf("应提示佩戴不可送: %s", res.Message)
	}
}
