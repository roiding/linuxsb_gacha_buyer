package site

import (
	"errors"
	"fmt"
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

// DonateResult 打赏结果。
type DonateResult struct {
	OK      bool
	Message string
}

// Donate 向主号帖子打赏 amount 积分。
// 协议：POST /donate，字段 _csrf + topic_id + request_key + amount + message。
// csrf 与 request_key 均从 GET /donate?topic_id=N 页面现取。
func (c *Client) Donate(topicID, amount int, message string) DonateResult {
	if amount <= 0 {
		return DonateResult{Message: "金额必须大于 0"}
	}
	status, body, err := c.get(fmt.Sprintf("/donate?topic_id=%d", topicID))
	if err != nil {
		return DonateResult{Message: "打开打赏页失败: " + err.Error()}
	}
	page := string(body)
	if status != http.StatusOK || strings.Contains(page, `name="password"`) {
		return DonateResult{Message: fmt.Sprintf("打赏页不可用（HTTP %d 或需登录）", status)}
	}
	mCSRF := regexp.MustCompile(`name="_csrf"\s+value="([0-9a-f]{64})"`).FindStringSubmatch(page)
	mKey := regexp.MustCompile(`name="request_key"\s+value="([0-9a-f]+)"`).FindStringSubmatch(page)
	if mCSRF == nil || mKey == nil {
		return DonateResult{Message: "打赏页缺少 _csrf 或 request_key"}
	}

	form := url.Values{}
	form.Set("_csrf", mCSRF[1])
	form.Set("topic_id", strconv.Itoa(topicID))
	form.Set("request_key", mKey[1])
	form.Set("amount", strconv.Itoa(amount))
	form.Set("message", message)

	st, _, err := c.postForm("/donate", form)
	if err != nil {
		return DonateResult{Message: "提交打赏失败: " + err.Error()}
	}
	switch {
	case st >= 300 && st < 400:
		// PRG：重定向即提交成功
		return DonateResult{OK: true, Message: "打赏成功"}
	case st == http.StatusOK:
		if msg := extractAlert(page); msg != "" && !strings.Contains(page, "已打赏") {
			return DonateResult{Message: msg}
		}
		return DonateResult{OK: true, Message: "打赏请求已提交"}
	default:
		return DonateResult{Message: fmt.Sprintf("HTTP %d", st)}
	}
}
