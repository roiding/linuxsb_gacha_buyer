package site

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"

	"gacha-buyer/internal/market"
)

// FetchTitleCatalog 抓取登录态下的全部称号目录。
func (c *Client) FetchTitleCatalog() ([]market.Title, error) {
	status, body, err := c.get("/gacha")
	if err != nil {
		return nil, fmt.Errorf("访问称号目录失败: %w", err)
	}
	if status >= 300 && status < 400 {
		return nil, errors.New("会话已失效（称号目录发生重定向），需要重新登录")
	}
	page := string(body)
	if strings.Contains(page, "name=\"password\"") && strings.Contains(page, "/login") {
		return nil, errors.New("会话已失效，需要重新登录")
	}
	return market.ParseTitleCatalog(page)
}

// FetchMarket 抓取并解析在售列表；未登录（跳 /login）时返回明确错误。
func (c *Client) FetchMarket() ([]market.Listing, error) {
	status, body, err := c.get("/gacha_market")
	if err != nil {
		return nil, fmt.Errorf("访问市场页失败: %w", err)
	}
	if status >= 300 && status < 400 {
		return nil, errors.New("会话已失效（市场页发生重定向），需要重新登录")
	}
	page := string(body)
	if strings.Contains(page, "name=\"password\"") && strings.Contains(page, "/login") {
		return nil, errors.New("会话已失效，需要重新登录")
	}
	listings, _, err := market.ParseMarket(page)
	if err != nil {
		return nil, err
	}
	return listings, nil
}

// FetchPoints 从已登录页面侧栏提取当前积分。
func (c *Client) FetchPoints() (int, error) {
	status, body, err := c.get("/gacha_profile")
	if err != nil {
		return 0, err
	}
	if status != 200 {
		return 0, fmt.Errorf("获取积分页失败 HTTP %d", status)
	}
	re := regexp.MustCompile(`积分\s*([,\d]+)`)
	m := re.FindStringSubmatch(string(body))
	if m == nil {
		return 0, errors.New("页面中未找到积分")
	}
	n, err := strconv.Atoi(strings.ReplaceAll(m[1], ",", ""))
	if err != nil {
		return 0, err
	}
	return n, nil
}

// BuyResult 单次购买结果。
type BuyResult struct {
	OK       bool
	Message  string
	HTTP     int
	Redirect string
}

// Buy 对指定 listing 下单。
func (c *Client) Buy(l market.Listing, quantity int) BuyResult {
	form := url.Values{}
	form.Set("_csrf", l.CSRF)
	form.Set("listing_id", strconv.Itoa(l.ListingID))
	form.Set("quantity", strconv.Itoa(quantity))

	req, err := http.NewRequest(http.MethodPost, c.base+"/gacha_market_buy",
		strings.NewReader(form.Encode()))
	if err != nil {
		return BuyResult{Message: "构造请求失败: " + err.Error()}
	}
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Referer", c.base+"/gacha_market")
	resp, err := c.http.Do(req)
	if err != nil {
		return BuyResult{Message: "请求失败: " + err.Error()}
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	page := string(body)

	res := BuyResult{HTTP: resp.StatusCode, Redirect: resp.Header.Get("Location")}
	switch {
	case resp.StatusCode >= 300 && resp.StatusCode < 400:
		// PRG 模式：重定向即提交成功（具体成败看后续页面，由调用方刷新确认）
		res.OK = true
		res.Message = "购买请求已提交"
	case resp.StatusCode == http.StatusOK && strings.Contains(page, "gacha-market-card"):
		// 直接渲染市场页：无错误提示视为成功
		if msg := extractAlert(page); msg != "" {
			res.Message = msg
			return res
		}
		res.OK = true
		res.Message = "购买请求已提交"
	default:
		res.Message = shortText(page, resp.StatusCode)
	}
	return res
}

// shortText 提取错误页可读信息。
func shortText(page string, status int) string {
	if msg := extractAlert(page); msg != "" {
		return msg
	}
	vis := extractVisible(page)
	vis = strings.Join(strings.Fields(vis), " ")
	if len(vis) > 120 {
		vis = vis[:120]
	}
	if vis != "" {
		return vis
	}
	return fmt.Sprintf("HTTP %d", status)
}
