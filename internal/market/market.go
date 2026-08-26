// Package market 解析 /gacha_market 页面的在售列表。
package market

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"golang.org/x/net/html"
)

// Rarity 稀有度枚举（与页面 class gacha-title-<rarity> 对应）。
type Rarity string

const (
	N   Rarity = "n"
	R   Rarity = "r"
	SR  Rarity = "sr"
	SSR Rarity = "ssr"
	UR  Rarity = "ur"
)

// Title 目录中的称号。
type Title struct {
	Name   string `json:"name"`
	Emoji  string `json:"emoji"`
	Rarity Rarity `json:"rarity"`
}

// Listing 市场在售条目。
type Listing struct {
	ListingID int    `json:"listing_id"` // listing_id
	TitleID   int    `json:"title_id"`   // title_id（购买表单未含，暂不使用）
	Name      string `json:"name"`       // 称号名（不含 emoji）
	Emoji     string `json:"emoji"`
	Rarity    Rarity `json:"rarity"`
	Price     int    `json:"price"`  // 单价
	Remain    int    `json:"remain"` // 剩余数量
	CSRF      string `json:"-"`      // 仅内部下单使用，不向 Web 泄露
}

// ParseMarket 从 /gacha_market 页面 HTML 提取全部在售条目与页面级 _csrf 兜底值。
func ParseMarket(page string) ([]Listing, string, error) {
	doc, err := html.Parse(strings.NewReader(page))
	if err != nil {
		return nil, "", fmt.Errorf("解析市场页失败: %w", err)
	}

	pageCSRF := ""
	var listings []Listing

	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if n.Type != html.ElementNode {
			goto children
		}
		switch n.Data {
		case "article":
			if l, ok := parseCard(n); ok {
				listings = append(listings, l)
			}
			return // 卡片内部无需再走全局逻辑
		case "input":
			if attr(n, "name") == "_csrf" && pageCSRF == "" {
				pageCSRF = attr(n, "value")
			}
		case "form":
			// 发布表单里的 _csrf 也可能先出现，统一由 input 分支处理
		}
	children:
		for ch := n.FirstChild; ch != nil; ch = ch.NextSibling {
			walk(ch)
		}
	}
	walk(doc)

	if len(listings) == 0 {
		// 新版空结果页仍有列表容器和筛选表单，属正常情况；
		// 连市场页特征都没有才是异常（未登录/被拦截）。
		if !strings.Contains(page, "gacha-market-grid") &&
			!strings.Contains(page, "gacha-market-filters") &&
			!strings.Contains(page, "gacha-market-card") {
			return listings, pageCSRF, fmt.Errorf("页面中未发现交易卡片（可能未登录或被拦截）")
		}
	}
	return listings, pageCSRF, nil
}

// parseCard 解析单个 <article class="gacha-market-card">。
func parseCard(card *html.Node) (Listing, bool) {
	var l Listing

	// 找购买表单
	var findForm func(*html.Node) *html.Node
	findForm = func(n *html.Node) *html.Node {
		if n.Type == html.ElementNode && n.Data == "form" && strings.Contains(attr(n, "class"), "gacha-market-buy") {
			return n
		}
		for ch := n.FirstChild; ch != nil; ch = ch.NextSibling {
			if r := findForm(ch); r != nil {
				return r
			}
		}
		return nil
	}
	form := findForm(card)
	if form == nil {
		return l, false
	}
	l.ListingID, _ = strconv.Atoi(inputValue(form, "listing_id"))
	l.CSRF = inputValue(form, "_csrf")
	if l.ListingID == 0 || l.CSRF == "" {
		return l, false
	}

	// 徽章（新版三段式结构）：
	// <span class="gacha-title-badge gacha-title-<rarity>">
	//   <span class="gacha-title-icon">emoji</span>
	//   <span class="gacha-title-name">名称</span>
	//   <span class="gacha-title-rarity">N</span>
	// </span>
	badge := findByClass(card, "span", "gacha-title-badge")
	if badge == nil {
		return l, false
	}
	l.Rarity = rarityFromClass(attr(badge, "class"))
	nameEl := findByClass(badge, "span", "gacha-title-name")
	if nameEl == nil {
		return l, false
	}
	l.Name = strings.TrimSpace(text(nameEl))
	if iconEl := findByClass(badge, "span", "gacha-title-icon"); iconEl != nil {
		l.Emoji = strings.TrimSpace(text(iconEl))
	}
	if l.Rarity == "" {
		if rEl := findByClass(badge, "span", "gacha-title-rarity"); rEl != nil {
			l.Rarity = Rarity(strings.ToLower(strings.TrimSpace(text(rEl))))
		}
	}

	// meta 区：单价 / 剩余
	meta := findByClass(card, "div", "gacha-market-meta")
	if meta == nil {
		return l, false
	}
	texts := strongTexts(meta)
	if len(texts) >= 1 {
		l.Price, _ = strconv.Atoi(texts[0])
	}
	if len(texts) >= 2 {
		l.Remain, _ = strconv.Atoi(texts[1])
	}
	if l.Price <= 0 {
		return l, false
	}
	return l, true
}

var reRarity = regexp.MustCompile(`gacha-title-([a-z]+)\b`)

func rarityFromClass(class string) Rarity {
	for _, m := range reRarity.FindAllStringSubmatch(class, -1) {
		r := Rarity(m[1])
		switch r {
		case N, R, SR, SSR, UR:
			return r
		}
	}
	return ""
}

func attr(n *html.Node, key string) string {
	for _, a := range n.Attr {
		if a.Key == key {
			return a.Val
		}
	}
	return ""
}

func inputValue(form *html.Node, name string) string {
	var found string
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if found != "" {
			return
		}
		if n.Type == html.ElementNode && n.Data == "input" && attr(n, "name") == name {
			found = attr(n, "value")
			return
		}
		for ch := n.FirstChild; ch != nil; ch = ch.NextSibling {
			walk(ch)
		}
	}
	walk(form)
	return found
}

func findByClass(root *html.Node, tag, classFragment string) *html.Node {
	var found *html.Node
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if found != nil {
			return
		}
		if n.Type == html.ElementNode && n.Data == tag && strings.Contains(attr(n, "class"), classFragment) {
			found = n
			return
		}
		for ch := n.FirstChild; ch != nil; ch = ch.NextSibling {
			walk(ch)
		}
	}
	walk(root)
	return found
}

func text(n *html.Node) string {
	var sb strings.Builder
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.TextNode {
			sb.WriteString(n.Data)
		}
		for ch := n.FirstChild; ch != nil; ch = ch.NextSibling {
			walk(ch)
		}
	}
	walk(n)
	return sb.String()
}

// ParseTitleCatalog 从 /gacha 页面提取全部称号目录。
func ParseTitleCatalog(page string) ([]Title, error) {
	doc, err := html.Parse(strings.NewReader(page))
	if err != nil {
		return nil, fmt.Errorf("解析称号目录失败: %w", err)
	}
	var out []Title
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.ElementNode && n.Data == "div" && strings.Contains(attr(n, "class"), "gacha-all-item") {
			if badge := findByClass(n, "span", "gacha-title-badge"); badge != nil {
				nameEl := findByClass(badge, "span", "gacha-title-name")
				if nameEl != nil {
					t := Title{Name: strings.TrimSpace(text(nameEl)), Rarity: rarityFromClass(attr(badge, "class"))}
					if iconEl := findByClass(badge, "span", "gacha-title-icon"); iconEl != nil {
						t.Emoji = strings.TrimSpace(text(iconEl))
					}
					if t.Rarity == "" {
						if rEl := findByClass(badge, "span", "gacha-title-rarity"); rEl != nil {
							t.Rarity = Rarity(strings.ToLower(strings.TrimSpace(text(rEl))))
						}
					}
					if t.Name != "" && t.Rarity != "" {
						out = append(out, t)
					}
				}
			}
			return
		}
		for ch := n.FirstChild; ch != nil; ch = ch.NextSibling {
			walk(ch)
		}
	}
	walk(doc)
	if len(out) == 0 && !strings.Contains(page, "gacha-all-item") {
		return nil, fmt.Errorf("页面中未发现称号目录（可能未登录或被拦截）")
	}
	return out, nil
}

// strongTexts 取节点内所有 <strong> 的文本（meta 区顺序：单价、剩余）。
func strongTexts(root *html.Node) []string {
	var out []string
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.ElementNode && n.Data == "strong" {
			out = append(out, strings.TrimSpace(text(n)))
		}
		for ch := n.FirstChild; ch != nil; ch = ch.NextSibling {
			walk(ch)
		}
	}
	walk(root)
	return out
}
