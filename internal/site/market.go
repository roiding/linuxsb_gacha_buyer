package site

import (
	"errors"
	"fmt"
	"html"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

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

// FetchMarket 抓取默认市场页（全部等级，最新发布）。
func (c *Client) FetchMarket() ([]market.Listing, error) {
	return c.FetchMarketFiltered("", "")
}

// FetchMarketFiltered 按稀有度与排序抓取市场在售列表。
// rarity: ""|n|r|sr|ssr|ur；sort: ""|latest|price_asc|price_desc。
// 市场页每类最多显示 100 条，配合分类+价格升序可覆盖"最新发布"100 条之外的便宜挂牌。
func (c *Client) FetchMarketFiltered(rarity, sort string) ([]market.Listing, error) {
	path := "/gacha_market"
	if rarity != "" || sort != "" {
		q := url.Values{}
		if rarity != "" {
			q.Set("rarity", rarity)
		}
		if sort != "" {
			q.Set("sort", sort)
		}
		path += "?" + q.Encode()
	}
	status, body, err := c.get(path)
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

// ---------------------------------------------------------------- 上架/下架 ----

// PublishableTitle 市场页发布表单中可出售的称号。
type PublishableTitle struct {
	TitleID  int    `json:"title_id"`
	Name     string `json:"name"`
	Sellable int    `json:"sellable"` // 可出售数量
}

var (
	rePublishForm  = regexp.MustCompile(`(?is)<form[^>]*action="/gacha_market_publish"[^>]*>(.*?)</form>`)
	reTitleOption  = regexp.MustCompile(`(?is)<option[^>]*value="(\d+)"[^>]*>(.*?)</option>`)
	reCancelForm   = regexp.MustCompile(`(?is)<form[^>]*action="/gacha_market_cancel"[^>]*>(.*?)</form>`)
	reListingInput = regexp.MustCompile(`name="listing_id"\s+value="(\d+)"`)
	reSellable     = regexp.MustCompile(`^(.*?)（可出售\s*(\d+)）`)
)

// FetchPublishableTitles 解析市场页发布表单下拉中可出售的称号（名称 + 可出售数量）。
func (c *Client) FetchPublishableTitles() ([]PublishableTitle, error) {
	status, body, err := c.get("/gacha_market")
	if err != nil {
		return nil, fmt.Errorf("访问市场页失败: %w", err)
	}
	page := string(body)
	if status >= 300 && status < 400 {
		return nil, errors.New("会话已失效（市场页发生重定向），需要重新登录")
	}
	if strings.Contains(page, "name=\"password\"") && strings.Contains(page, "/login") {
		return nil, errors.New("会话已失效，需要重新登录")
	}
	m := rePublishForm.FindStringSubmatch(page)
	if m == nil {
		return nil, errors.New("市场页未找到发布表单（可能没有可出售称号）")
	}
	var out []PublishableTitle
	for _, om := range reTitleOption.FindAllStringSubmatch(m[1], -1) {
		id, _ := strconv.Atoi(om[1])
		name, sellable := parsePublishOption(html.UnescapeString(om[2]))
		if id <= 0 || name == "" {
			continue
		}
		out = append(out, PublishableTitle{TitleID: id, Name: name, Sellable: sellable})
	}
	return out, nil
}

// parsePublishOption 解析发布下拉选项文本 "🍀 欧皇（可出售 1）"。
func parsePublishOption(text string) (name string, sellable int) {
	text = strings.TrimSpace(stripLeadingEmoji(text))
	if m := reSellable.FindStringSubmatch(text); m != nil {
		name = strings.TrimSpace(stripLeadingEmoji(m[1]))
		sellable, _ = strconv.Atoi(m[2])
	} else {
		name = text
		sellable = 1
	}
	return name, sellable
}

// stripLeadingEmoji 去掉字符串开头的表情符号/装饰 rune，得到纯称号名。
func stripLeadingEmoji(s string) string {
	for s != "" {
		r, size := utf8.DecodeRuneInString(s)
		if r == utf8.RuneError || unicode.IsLetter(r) || unicode.IsDigit(r) {
			break
		}
		s = s[size:]
	}
	return strings.TrimSpace(s)
}

// PublishResult 上架结果。
type PublishResult struct {
	OK      bool
	Message string
	TitleID int
	Qty     int
	Price   int
}

// PublishTitle 发布一条交易（上架称号）。
func (c *Client) PublishTitle(titleID, quantity, unitPrice, durationHours int) PublishResult {
	status, body, err := c.get("/gacha_market")
	if err != nil {
		return PublishResult{TitleID: titleID, Message: "打开市场页失败: " + err.Error()}
	}
	page := string(body)
	if status != http.StatusOK || isLoginPage(page) {
		return PublishResult{TitleID: titleID, Message: "市场页不可用（HTTP %d 或需登录）"}
	}
	csrf, ok := pageCSRF(page)
	if !ok {
		return PublishResult{TitleID: titleID, Message: "市场页缺少 _csrf"}
	}
	form := url.Values{}
	form.Set("_csrf", csrf)
	form.Set("title_id", strconv.Itoa(titleID))
	form.Set("quantity", strconv.Itoa(quantity))
	form.Set("unit_price", strconv.Itoa(unitPrice))
	form.Set("duration_hours", strconv.Itoa(durationHours))
	st, resp, err := c.postForm("/gacha_market_publish", form)
	ok, msg := parseMarketActionResult(st, resp, err)
	return PublishResult{OK: ok, Message: msg, TitleID: titleID, Qty: quantity, Price: unitPrice}
}

// CancelResult 撤回（下架）结果。
type CancelResult struct {
	OK        bool
	Message   string
	ListingID int
}

// CancelListing 撤回一条我的在售挂牌（下架）。
func (c *Client) CancelListing(listingID int) CancelResult {
	status, body, err := c.get("/gacha_market")
	if err != nil {
		return CancelResult{ListingID: listingID, Message: "打开市场页失败: " + err.Error()}
	}
	page := string(body)
	if status != http.StatusOK || isLoginPage(page) {
		return CancelResult{ListingID: listingID, Message: "市场页不可用（HTTP %d 或需登录）"}
	}
	csrf, ok := pageCSRF(page)
	if !ok {
		return CancelResult{ListingID: listingID, Message: "市场页缺少 _csrf"}
	}
	form := url.Values{}
	form.Set("_csrf", csrf)
	form.Set("listing_id", strconv.Itoa(listingID))
	st, resp, err := c.postForm("/gacha_market_cancel", form)
	ok, msg := parseMarketActionResult(st, resp, err)
	return CancelResult{OK: ok, Message: msg, ListingID: listingID}
}

// MyListingIDs 解析市场页"我的交易"区域中全部在售挂牌的 listing_id（含撤回按钮的）。
func (c *Client) MyListingIDs() ([]int, error) {
	status, body, err := c.get("/gacha_market")
	if err != nil {
		return nil, fmt.Errorf("访问市场页失败: %w", err)
	}
	page := string(body)
	if status >= 300 && status < 400 {
		return nil, errors.New("会话已失效（市场页发生重定向），需要重新登录")
	}
	if strings.Contains(page, "name=\"password\"") && strings.Contains(page, "/login") {
		return nil, errors.New("会话已失效，需要重新登录")
	}
	seen := map[int]bool{}
	var out []int
	for _, fm := range reCancelForm.FindAllStringSubmatch(page, -1) {
		if lm := reListingInput.FindStringSubmatch(fm[1]); lm != nil {
			id, _ := strconv.Atoi(lm[1])
			if id > 0 && !seen[id] {
				seen[id] = true
				out = append(out, id)
			}
		}
	}
	return out, nil
}

// parseMarketActionResult 统一解析上架/下架 POST 的响应文案。
func parseMarketActionResult(status int, body []byte, err error) (bool, string) {
	if err != nil {
		return false, err.Error()
	}
	page := string(body)
	if isLoginPage(page) {
		return false, "会话已失效"
	}
	if status >= 400 {
		return false, shortText(page, status)
	}
	if msg := extractAlert(page); isMarketActionFailure(msg) {
		return false, msg
	} else if msg != "" {
		return true, msg
	}
	return true, ""
}

func isMarketActionFailure(message string) bool {
	if message == "" {
		return false
	}
	for _, word := range []string{
		"失败", "错误", "无效", "不足", "售罄", "不能", "无法", "不存在",
		"拒绝", "未登录", "上限", "不允许", "非法",
	} {
		if strings.Contains(message, word) {
			return true
		}
	}
	return false
}
