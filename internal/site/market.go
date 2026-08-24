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

// BuyResult 单次购买结果。OK 只表示已经确认成交，不表示请求已提交。
type BuyResult struct {
	OK          bool
	Submitted   bool
	Message     string
	HTTP        int
	Redirect    string
	Qty         int
	Cost        int
	PointsAfter int
}

// Buy 对指定 listing 下单，并通过积分变化确认实际成交数量。
func (c *Client) Buy(l market.Listing, quantity, pointsBefore int) BuyResult {
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
	body, readErr := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if readErr != nil {
		return BuyResult{HTTP: resp.StatusCode, Message: "读取购买响应失败: " + readErr.Error()}
	}
	page := string(body)
	res := BuyResult{
		HTTP:        resp.StatusCode,
		Redirect:    resp.Request.URL.String(),
		Submitted:   resp.StatusCode >= 200 && resp.StatusCode < 400,
		Qty:         quantity,
		Cost:        quantity * l.Price,
		PointsAfter: pointsBefore,
	}
	if strings.Contains(page, "name=\"password\"") && strings.Contains(page, "/login") {
		res.Submitted = false
		res.Message = "购买请求后会话已失效"
		return res
	}
	responseMessage := extractAlert(page)
	if resp.StatusCode >= 400 {
		res.Submitted = false
		res.Message = shortText(page, resp.StatusCode)
		return res
	}
	if isPurchaseFailure(responseMessage) {
		res.Submitted = false
		res.Message = responseMessage
		return res
	}

	pointsAfter, err := c.FetchPoints()
	if err != nil {
		res.Message = "购买请求已提交，但无法确认余额变化: " + err.Error()
		if responseMessage != "" {
			res.Message += "；站点返回: " + responseMessage
		}
		return res
	}
	res.PointsAfter = pointsAfter
	delta := pointsBefore - pointsAfter
	if delta > 0 && l.Price > 0 && delta%l.Price == 0 {
		actualQty := delta / l.Price
		if actualQty <= quantity {
			res.OK = true
			res.Qty = actualQty
			res.Cost = delta
			res.Message = fmt.Sprintf("成交已确认：余额 %d → %d", pointsBefore, pointsAfter)
			if actualQty != quantity {
				res.Message += fmt.Sprintf("，实际成交 %d/%d 个", actualQty, quantity)
			}
			return res
		}
	}
	if responseMessage != "" {
		res.Message = responseMessage + fmt.Sprintf("；余额未出现对应扣款（%d → %d），未计入成交", pointsBefore, pointsAfter)
	} else {
		res.Message = fmt.Sprintf("购买请求已提交，但余额未出现对应扣款（%d → %d），未确认成交", pointsBefore, pointsAfter)
	}
	return res
}

func isPurchaseFailure(message string) bool {
	if message == "" {
		return false
	}
	for _, word := range []string{"失败", "错误", "无效", "不足", "售罄", "不能", "无法", "不存在", "拒绝", "已被购买", "未登录"} {
		if strings.Contains(message, word) {
			return true
		}
	}
	return false
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
