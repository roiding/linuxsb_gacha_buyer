package site

// gacha 相关：每日免费抽卡 + 把抽到的称号赠送给其他用户。
// 端点与表单结构均从页面实际解析（与登录/打赏/回帖同一套路）；
// 站点改版时调整本文件顶部常量与正则即可，无需改调用方。

import (
	"encoding/json"
	"fmt"
	"html"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
)

const (
	gachaPagePath    = "/gacha"
	gachaProfilePath = "/gacha_profile"
	gachaPullPath    = "/gacha_pull"
	gachaGiftPath    = "/gacha_gift"
)

var (
	rePageTitle   = regexp.MustCompile(`(?is)<title>(.*?)</title>`)
	reGachaCSRF   = regexp.MustCompile(`name="_csrf"\s+value="([0-9a-fA-F]+)"`)
	reResultName  = regexp.MustCompile(`(?is)class="gacha-result-name"[^>]*>([^<]+)<`)
	reProfileItem = regexp.MustCompile(`(?is)<div class="gacha-profile-item[^"]*"`)
	reTitleName   = regexp.MustCompile(`(?is)class="gacha-title-name"[^>]*>([^<]+)<`)
	reGiftCall    = regexp.MustCompile(`gachaGiftModalOpen\((\{.*\})\)`)
	reCountTag    = regexp.MustCompile(`×\s*(\d+)`)
	reFormBlock   = regexp.MustCompile(`(?is)<form[^>]*action="([^"]*)"[^>]*>(.*?)</form>`)
)

// GachaDrawResult 免费抽卡结果。OK 表示今日抽卡已落定（避免重复抽）。
type GachaDrawResult struct {
	OK      bool   // true=今日免费一抽已完成（无论是否空包）
	Drawn   bool   // true=本次真实执行了一次抽卡
	Title   string // 抽到的称号名；空=空包/无所得
	Message string
}

// DrawGacha 执行每日免费一抽。页面已无“今日免费一抽”按钮（今日已抽）时不发请求。
func (c *Client) DrawGacha() GachaDrawResult {
	status, body, err := c.get(gachaPagePath)
	if err != nil {
		return GachaDrawResult{Message: "打开抽卡页失败: " + err.Error()}
	}
	page := string(body)
	if status != http.StatusOK || isLoginPage(page) {
		return GachaDrawResult{Message: fmt.Sprintf("抽卡页不可用（HTTP %d 或需登录）", status)}
	}
	csrf, found := findFreePullCSRF(page)
	if !found {
		if strings.Contains(page, "免费") || strings.Contains(page, "今日免费一抽") {
			return GachaDrawResult{Message: "未找到免费抽表单（页面仍含免费抽字样），抽卡页结构可能已变化"}
		}
		return GachaDrawResult{OK: true, Drawn: false, Message: "今日免费一抽已完成（页面无免费抽按钮）"}
	}
	form := url.Values{}
	form.Set("_csrf", csrf)
	st, respBody, err := c.postForm(gachaPullPath, form)
	if err != nil {
		return GachaDrawResult{Message: "提交抽卡请求失败: " + err.Error()}
	}
	res := parseGachaResult(string(respBody))
	if st >= 400 {
		res.OK, res.Drawn = false, false
		res.Message = shortText(string(respBody), st)
	}
	return res
}

// parseGachaResult 解析抽卡结果页（/gacha_pull?result=…）。
// 中奖卡片含 gacha-result-name；空包卡片无该元素且含“什么都没抽到”。
// POST 已到达结果页即视为抽卡已消费（OK=true），解析失败也绝不重复抽。
func parseGachaResult(page string) GachaDrawResult {
	res := GachaDrawResult{OK: true, Drawn: true}
	if isLoginPage(page) {
		res.OK, res.Drawn = false, false
		res.Message = "抽卡请求后会话已失效"
		return res
	}
	if strings.Contains(page, "什么都没抽到") {
		res.Message = "空包，什么都没抽到"
		return res
	}
	if m := reResultName.FindStringSubmatch(page); m != nil {
		if name := strings.TrimSpace(html.UnescapeString(m[1])); name != "" {
			res.Title = name
			res.Message = "获得称号「" + name + "」"
			return res
		}
	}
	res.Message = "抽卡已消费，但结果未识别（页面: " + visibleSnippet(page, 60) + "）"
	return res
}

// findFreePullCSRF 从 /gacha 页定位“今日免费一抽”表单并返回其 _csrf。
// 付费单抽（抽一次 10 积分）与十连（/gacha_pull_10）均被排除。
func findFreePullCSRF(page string) (string, bool) {
	for _, m := range reFormBlock.FindAllStringSubmatch(page, -1) {
		action := html.UnescapeString(m[1])
		if !strings.Contains(action, "pull") || strings.Contains(action, "pull_10") {
			continue
		}
		inner := m[2]
		if !strings.Contains(inner, "免费") {
			continue
		}
		if csrf := reGachaCSRF.FindStringSubmatch(inner); csrf != nil {
			return csrf[1], true
		}
	}
	return "", false
}

// GiftResult 赠送称号结果。OK 表示请求已提交且未被判定为失败。
type GiftResult struct {
	OK      bool
	Gifted  bool // true=已确认赠送成功
	Target  string
	TitleID int
	Message string
}

// ProfileTitle 小号已拥有的一个称号（来自 /gacha_profile）。
type ProfileTitle struct {
	ID       int    // 赠送/装备用的 title_id
	Name     string // 称号名（服务端明文）
	Count    int    // 拥有数量（× N）
	Equipped bool   // 是否正在佩戴
}

// GiftTitle 把本账号已拥有的 titleName 赠送 1 张给 target 用户。
// 只操作名称完全匹配的称号；可赠送数 = 总数 − 佩戴中的 1 张，不足 1 张不赠送。
// title_id 从 /gacha_profile 对应卡片的赠送按钮解析。
func (c *Client) GiftTitle(target, titleName string) GiftResult {
	if target == "" {
		return GiftResult{Message: "赠送目标用户名为空"}
	}
	status, body, err := c.get(gachaProfilePath)
	if err != nil {
		return GiftResult{Message: "打开我的称号页失败: " + err.Error()}
	}
	page := string(body)
	if status != http.StatusOK || isLoginPage(page) {
		return GiftResult{Message: fmt.Sprintf("我的称号页不可用（HTTP %d 或需登录）", status)}
	}
	id, giftable, found := findGiftableTitle(parseProfileTitles(page), titleName)
	if !found {
		return GiftResult{Message: fmt.Sprintf("我的称号页未找到「%s」", titleName)}
	}
	if giftable < 1 {
		return GiftResult{Message: fmt.Sprintf("「%s」仅 1 张且正在佩戴，无法赠送", titleName)}
	}
	csrf, ok := pageCSRF(page)
	if !ok {
		return GiftResult{Message: "我的称号页缺少 _csrf"}
	}
	form := url.Values{}
	form.Set("_csrf", csrf)
	form.Set("title_id", strconv.Itoa(id))
	form.Set("username", target)
	st, resp, err := c.postForm(gachaGiftPath, form)
	if err != nil {
		return GiftResult{OK: false, Target: target, TitleID: id, Message: "赠送请求异常: " + err.Error()}
	}
	res := GiftResult{OK: true, Target: target, TitleID: id}
	respPage := string(resp)
	if isLoginPage(respPage) {
		res.OK = false
		res.Message = "赠送请求后会话已失效"
		return res
	}
	if strings.Contains(respPage, "已赠送给") {
		res.Gifted = true
		res.Message = "已赠送给 " + target
		return res
	}
	if st >= 400 {
		res.OK = false
		res.Message = shortText(respPage, st)
		return res
	}
	if msg := extractAlert(respPage); isGiftFailure(msg) {
		res.OK = false
		res.Message = msg
		return res
	} else if msg != "" {
		res.Message = msg
	}
	// 2xx 且无失败文案 → 乐观确认成功
	res.Gifted = true
	if res.Message == "" {
		res.Message = "赠送请求已提交"
	}
	return res
}

// parseProfileTitles 解析 /gacha_profile 的全部已拥有称号（含正在佩戴的）。
func parseProfileTitles(page string) []ProfileTitle {
	var out []ProfileTitle
	for _, part := range splitProfileItems(page) {
		nm := reTitleName.FindStringSubmatch(part)
		if nm == nil {
			continue
		}
		t := ProfileTitle{
			Name:     strings.TrimSpace(html.UnescapeString(nm[1])),
			Count:    1,
			Equipped: strings.Contains(part, "gacha-equipped-label"),
		}
		if cm := reCountTag.FindStringSubmatch(part); cm != nil {
			if n, err := strconv.Atoi(cm[1]); err == nil && n > 0 {
				t.Count = n
			}
		}
		if bm := reGiftCall.FindStringSubmatch(part); bm != nil {
			var info struct {
				ID int `json:"id"`
			}
			// 页面里 onclick 是 HTML 实体 + JSON \uXXXX 转义，先反转义再解析
			if err := json.Unmarshal([]byte(html.UnescapeString(bm[1])), &info); err == nil && info.ID > 0 {
				t.ID = info.ID
			}
		}
		out = append(out, t)
	}
	return out
}

// findGiftableTitle 在已拥有称号中找名称匹配的称号。
// 返回其 id 与可赠送数量（总数扣减佩戴中的 1 张）。
func findGiftableTitle(titles []ProfileTitle, drawnTitle string) (id, giftable int, found bool) {
	for _, t := range titles {
		if t.Name != drawnTitle {
			continue
		}
		n := t.Count
		if t.Equipped {
			n--
		}
		return t.ID, n, true
	}
	return 0, 0, false
}

func splitProfileItems(page string) []string {
	idx := reProfileItem.FindAllStringIndex(page, -1)
	if len(idx) == 0 {
		return nil
	}
	out := make([]string, 0, len(idx))
	for i, m := range idx {
		end := len(page)
		if i+1 < len(idx) {
			end = idx[i+1][0]
		}
		out = append(out, page[m[1]:end])
	}
	return out
}

func pageCSRF(page string) (string, bool) {
	if m := reGachaCSRF.FindStringSubmatch(page); m != nil {
		return m[1], true
	}
	return "", false
}

func isGiftFailure(message string) bool {
	if message == "" {
		return false
	}
	for _, word := range []string{"失败", "错误", "无效", "不足", "不存在", "不能", "无法", "拒绝", "未登录", "上限", "不允许"} {
		if strings.Contains(message, word) {
			return true
		}
	}
	return false
}

// UsernameFromProfilePage 从 /user/{uid} 页面提取显示用户名。
// 页面 title 形如“用户名 - 站点名 - …”，取第一个“ - ”前的内容。
func UsernameFromProfilePage(page string) string {
	m := rePageTitle.FindStringSubmatch(page)
	if m == nil {
		return ""
	}
	title := strings.TrimSpace(html.UnescapeString(m[1]))
	if i := strings.Index(title, " - "); i > 0 {
		title = strings.TrimSpace(title[:i])
	}
	return title
}

// visibleSnippet 提取页面可见文本的片段（用于诊断消息）。
func visibleSnippet(page string, max int) string {
	s := strings.Join(strings.Fields(extractVisible(page)), " ")
	if len(s) > max {
		s = s[:max] + "…"
	}
	return s
}
