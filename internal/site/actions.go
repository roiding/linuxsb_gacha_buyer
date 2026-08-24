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
)

var reSideUID = regexp.MustCompile(`href="/user/(\d+)\?tab=points_rewards"`)

// GetMyUID 从侧栏"我的积分"链接提取当前账号 UID。
func (c *Client) GetMyUID() (int, error) {
	status, body, err := c.get("/")
	if err != nil {
		return 0, err
	}
	if status != http.StatusOK {
		return 0, fmt.Errorf("首页 HTTP %d", status)
	}
	m := reSideUID.FindStringSubmatch(string(body))
	if m == nil {
		return 0, errors.New("侧栏未找到用户入口（可能未登录）")
	}
	return strconv.Atoi(m[1])
}

// CheckIn 触发每日签到。站点为服务端实现：带登录态访问任意页面即完成当日签到。
// 返回签到前后的积分流水是否出现今日记录无意义，这里仅确保触发了一次页面访问。
func (c *Client) CheckIn() error {
	status, _, err := c.get("/")
	if err != nil {
		return fmt.Errorf("签到（访问首页）失败: %w", err)
	}
	if status != http.StatusOK {
		return fmt.Errorf("签到（访问首页）HTTP %d", status)
	}
	return nil
}

// Logout 退出站点登录：POST /logout（需 _csrf），并清空本地会话。
func (c *Client) Logout() error {
	status, body, err := c.get("/profile")
	if err != nil && status != http.StatusOK {
		// 拿不到 _csrf 就直接清本地 cookie 兜底
		c.clearCookies()
		return fmt.Errorf("取退出令牌失败: %w", err)
	}
	page := string(body)
	m := regexp.MustCompile(`name="_csrf"\s+value="([0-9a-f]{64})"`).FindStringSubmatch(page)
	if m != nil {
		form := url.Values{}
		form.Set("_csrf", m[1])
		_, _, _ = c.postForm("/logout", form)
	}
	c.clearCookies()
	c.logf("已退出站点登录并清空本地会话")
	return nil
}

// clearCookies 清空内存中的会话 cookie。
func (c *Client) clearCookies() {
	u, _ := url.Parse(c.base)
	c.jar.SetCookies(u, nil)
}

// DonateResult 单次打赏结果。OK 只表示已经通过余额变化确认成功。
type DonateResult struct {
	OK           bool
	Confirmed    bool
	Submitted    bool
	Pending      bool
	Retryable    bool
	HTTP         int
	PointsBefore int
	PointsAfter  int
	Message      string
}

// Donate 向主号帖子打赏 amount 积分，并通过提交前后的余额确认结果。
func (c *Client) Donate(topicID, amount int, message string) DonateResult {
	before, err := c.FetchPoints()
	if err != nil {
		return DonateResult{Message: "提交前查询余额失败: " + err.Error()}
	}
	return c.DonateWithBalance(topicID, amount, message, before)
}

// DonateWithBalance 使用调用方已经读取的提交前余额执行打赏。
func (c *Client) DonateWithBalance(topicID, amount int, message string, pointsBefore int) DonateResult {
	res := DonateResult{PointsBefore: pointsBefore, Retryable: true}
	if amount <= 0 {
		res.Retryable = false
		res.Message = "金额必须大于 0"
		return res
	}
	status, body, err := c.get(fmt.Sprintf("/donate?topic_id=%d", topicID))
	if err != nil {
		res.Retryable = true
		res.Message = "打开打赏页失败: " + err.Error()
		return res
	}
	page := string(body)
	if status != http.StatusOK || isLoginPage(page) {
		res.HTTP = status
		res.Message = fmt.Sprintf("打赏页不可用（HTTP %d 或需登录）", status)
		return res
	}
	mCSRF := regexp.MustCompile(`name="_csrf"\s+value="([0-9a-f]{64})"`).FindStringSubmatch(page)
	mKey := regexp.MustCompile(`name="request_key"\s+value="([0-9a-f]+)"`).FindStringSubmatch(page)
	if mCSRF == nil || mKey == nil {
		res.Retryable = true
		res.Message = "打赏页缺少 _csrf 或 request_key"
		return res
	}
	form := url.Values{}
	form.Set("_csrf", mCSRF[1])
	form.Set("topic_id", strconv.Itoa(topicID))
	form.Set("request_key", mKey[1])
	form.Set("amount", strconv.Itoa(amount))
	form.Set("message", message)

	st, responseBody, finalURL, err := c.postDonate(form)
	res.HTTP = st
	if err != nil {
		res.Pending = true
		res.Message = "打赏请求结果未知，正在尝试核验余额: " + err.Error()
		return c.confirmDonateBalance(res, amount)
	}
	res.Submitted = st >= 200 && st < 400
	responsePage := string(responseBody)
	if isLoginPage(responsePage) || strings.Contains(finalURL, "/login") {
		res.Submitted = false
		res.Retryable = true
		res.Message = "打赏请求后会话已失效"
		return res
	}
	if st >= 400 {
		res.Retryable = st >= 500
		res.Message = shortText(responsePage, st)
		return c.confirmDonateBalance(res, amount)
	}
	if msg := extractAlert(responsePage); isDonateFailure(msg) {
		res.Submitted = false
		res.Retryable = isDonateRetryable(msg)
		res.Message = msg
		return res
	}
	return c.confirmDonateBalance(res, amount)
}

func (c *Client) postDonate(form url.Values) (int, []byte, string, error) {
	req, err := http.NewRequest(http.MethodPost, c.base+"/donate", strings.NewReader(form.Encode()))
	if err != nil {
		return 0, nil, "", err
	}
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Referer", c.base+"/donate?topic_id="+form.Get("topic_id"))
	resp, err := c.http.Do(req)
	if err != nil {
		return 0, nil, "", err
	}
	defer resp.Body.Close()
	body, readErr := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if readErr != nil {
		return resp.StatusCode, body, resp.Request.URL.String(), readErr
	}
	return resp.StatusCode, body, resp.Request.URL.String(), nil
}

func (c *Client) confirmDonateBalance(res DonateResult, amount int) DonateResult {
	after, err := c.FetchPoints()
	if err != nil {
		res.Pending = true
		res.Retryable = false
		if res.Message == "" {
			res.Message = "打赏请求已提交，但无法确认余额变化: " + err.Error()
		} else {
			res.Message += "；余额核验失败: " + err.Error()
		}
		return res
	}
	res.PointsAfter = after
	if res.PointsBefore-after == amount {
		res.OK = true
		res.Confirmed = true
		res.Retryable = false
		res.Pending = false
		res.Message = fmt.Sprintf("打赏已确认：余额 %d → %d", res.PointsBefore, after)
		return res
	}
	if res.Submitted {
		res.Pending = true
		res.Retryable = false
		res.Message = fmt.Sprintf("打赏请求已提交，但余额未出现对应扣减（%d → %d）", res.PointsBefore, after)
	} else if after == res.PointsBefore && res.Retryable {
		res.Pending = false
		res.Message += fmt.Sprintf("；余额确认未扣减（%d → %d），允许安全重试", res.PointsBefore, after)
	} else if res.Message == "" {
		res.Message = fmt.Sprintf("打赏未确认，余额未出现对应扣减（%d → %d）", res.PointsBefore, after)
	}
	return res
}

func isLoginPage(page string) bool {
	return strings.Contains(page, `name="password"`) && strings.Contains(page, "/login")
}

func isDonateHardFailure(message string) bool {
	for _, word := range []string{
		"注册未满", "注册时间不足", "账号未满", "未满三天", "未满3天", "三天后",
		"资格不足", "不满足条件", "活动资格", "积分不足", "余额不足", "超过上限",
		"达到上限", "不能给自己", "禁止打赏", "无权", "权限不足", "已关闭",
	} {
		if strings.Contains(message, word) {
			return true
		}
	}
	return false
}

func isDonateRetryable(message string) bool {
	return !isDonateHardFailure(message)
}

func isDonateFailure(message string) bool {
	if message == "" {
		return false
	}
	if isDonateHardFailure(message) {
		return true
	}
	for _, word := range []string{"失败", "错误", "无效", "不足", "不能", "无法", "不存在", "拒绝", "未登录", "上限"} {
		if strings.Contains(message, word) {
			return true
		}
	}
	return false
}
