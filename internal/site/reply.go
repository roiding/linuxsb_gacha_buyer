package site

import (
	"errors"
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"golang.org/x/net/html"
)

// ReplyResult 单次回帖结果。Confirmed 只表示页面确认出现了新 replyid。
type ReplyResult struct {
	Submitted    bool
	Confirmed    bool
	Retryable    bool
	Message      string
	HTTP         int
	ReplyID      int
	NeedsCaptcha bool
}

type replyForm struct {
	action          string
	fields          url.Values
	captchaToken    string
	captchaPow      string
	captchaQuestion string
}

var replyIDRE = regexp.MustCompile(`/topic/([1-9][0-9]*)\?replyid=([1-9][0-9]*)`)
var replyCaptchaDelay = true

// SetReplyCaptchaDelayForTest controls the reply submission delay in tests.
func SetReplyCaptchaDelayForTest(enabled bool) func() {
	old := replyCaptchaDelay
	replyCaptchaDelay = enabled
	return func() { replyCaptchaDelay = old }
}

func waitReplySubmitDelay() {
	if replyCaptchaDelay {
		time.Sleep(time.Duration(3_000+rand.Intn(3_000)) * time.Millisecond)
	}
}

func replyFailureRetryable(status int, message string) bool {
	if status == http.StatusTooManyRequests || status >= 500 {
		return true
	}
	low := strings.ToLower(message)
	for _, word := range []string{"提交过快", "验证码加载", "等待验证码", "请求过于频繁", "请稍后再试"} {
		if strings.Contains(low, strings.ToLower(word)) {
			return true
		}
	}
	return false
}

// Reply 回答指定主题。topicURL 必须是站内 /topic/<id> 链接。
func (c *Client) Reply(topicURL, message string) ReplyResult {
	form, topicID, err := c.fetchReplyForm(topicURL)
	if err != nil {
		return ReplyResult{Message: err.Error()}
	}
	if len([]rune(strings.TrimSpace(message))) < 5 {
		return ReplyResult{Message: "回复内容至少需要 5 个字"}
	}
	form.fields.Set("body", message)
	if form.captchaToken != "" {
		payload := tokenPayload(form.captchaToken)
		form.captchaQuestion = jsonStr(payload, "question")
		form.captchaPow = jsonStr(payload, "pow")
		answer, err := SolveCaptcha(form.captchaQuestion)
		if err != nil {
			return ReplyResult{NeedsCaptcha: true, Message: "验证码求解失败: " + err.Error()}
		}
		form.fields.Set("native_captcha_answer", answer)
		if form.captchaPow != "" {
			payload := tokenPayload(form.captchaToken)
			zeros := jsonInt(payload, "zeros")
			if zeros > 0 {
				nonce, err := SolvePoW(form.captchaPow, zeros)
				if err != nil {
					return ReplyResult{NeedsCaptcha: true, Message: "验证码 PoW 求解失败: " + err.Error()}
				}
				form.fields.Set("native_captcha_pow", nonce)
			}
		}
	}

	waitReplySubmitDelay()

	status, body, finalURL, err := c.postReply(form.action, "/topic/"+strconv.Itoa(topicID), form.fields)
	res := ReplyResult{HTTP: status, NeedsCaptcha: form.captchaToken != ""}
	if err != nil {
		res.Message = "提交回复失败: " + err.Error()
		return res
	}
	res.Submitted = status >= 200 && status < 400
	if status >= 400 {
		res.Submitted = false
		res.Message = shortText(string(body), status)
		res.Retryable = replyFailureRetryable(status, res.Message)
		return res
	}

	page := string(body)
	if strings.Contains(page, `name="password"`) && strings.Contains(page, "/login") {
		res.Submitted = false
		res.Message = "回复后会话已失效"
		return res
	}
	if msg := extractAlert(page); msg != "" && isReplyFailure(msg) {
		res.Submitted = false
		res.Message = msg
		res.Retryable = replyFailureRetryable(0, msg)
		return res
	}
	if m := replyIDRE.FindStringSubmatch(finalURL); m != nil {
		gotTopic, _ := strconv.Atoi(m[1])
		if gotTopic == topicID {
			res.Confirmed = true
			res.ReplyID, _ = strconv.Atoi(m[2])
			res.Message = fmt.Sprintf("回复已确认，replyid=%d", res.ReplyID)
			return res
		}
	}
	if msg := extractAlert(page); msg != "" {
		res.Message = msg + "；已提交但未确认回复"
	} else {
		res.Message = "回复请求已提交，但未确认生成 replyid"
	}
	return res
}

// ReplyWithRetry 对明确未提交且可安全重试的失败做一次重试。
func (c *Client) ReplyWithRetry(topicURL, message string) ReplyResult {
	res := c.Reply(topicURL, message)
	if !res.Retryable || res.Submitted || res.Confirmed {
		return res
	}
	waitReplySubmitDelay()
	// 只重试一次，避免重复扣款/重复提交风险。
	return c.Reply(topicURL, message)
}

func (c *Client) fetchReplyForm(topicURL string) (*replyForm, int, error) {
	u, err := url.Parse(topicURL)
	if err != nil {
		return nil, 0, errors.New("抽奖帖 URL 不合法")
	}
	parts := strings.Split(strings.Trim(u.Path, "/"), "/")
	if len(parts) != 2 || parts[0] != "topic" {
		return nil, 0, errors.New("抽奖帖 URL 必须是 /topic/<id>")
	}
	topicID, err := strconv.Atoi(parts[1])
	if err != nil || topicID <= 0 {
		return nil, 0, errors.New("抽奖帖 ID 不合法")
	}
	status, body, err := c.get("/topic/" + strconv.Itoa(topicID))
	if err != nil {
		return nil, 0, fmt.Errorf("访问抽奖帖失败: %w", err)
	}
	if status != http.StatusOK {
		return nil, 0, fmt.Errorf("抽奖帖返回 HTTP %d", status)
	}
	form, err := parseReplyForm(string(body), topicID)
	if err != nil {
		return nil, 0, err
	}
	return form, topicID, nil
}

func parseReplyForm(page string, topicID int) (*replyForm, error) {
	doc, err := html.Parse(strings.NewReader(page))
	if err != nil {
		return nil, errors.New("解析抽奖帖失败")
	}
	var found *replyForm
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if found != nil {
			return
		}
		if n.Type == html.ElementNode && n.Data == "form" {
			candidate := &replyForm{action: attr(n, "action"), fields: url.Values{}}
			var hasBody, hasTopic bool
			for ch := n.FirstChild; ch != nil; ch = ch.NextSibling {
				collectReplyFields(ch, candidate, &hasBody, &hasTopic)
			}
			if hasBody && hasTopic && candidate.fields.Get("topic_id") == strconv.Itoa(topicID) {
				if candidate.action == "" {
					candidate.action = "/reply_edit"
				}
				if !strings.HasPrefix(candidate.action, "/") {
					candidate.action = "/" + candidate.action
				}
				found = candidate
				return
			}
		}
		for ch := n.FirstChild; ch != nil; ch = ch.NextSibling {
			walk(ch)
		}
	}
	walk(doc)
	if found == nil {
		return nil, errors.New("主题页未找到回复表单（可能未登录或已关闭回复）")
	}
	return found, nil
}

func collectReplyFields(n *html.Node, form *replyForm, hasBody, hasTopic *bool) {
	if n.Type == html.ElementNode {
		switch n.Data {
		case "textarea":
			if name := attr(n, "name"); name == "body" {
				*hasBody = true
			}
		case "input":
			name := attr(n, "name")
			if name != "" {
				form.fields.Set(name, attr(n, "value"))
			}
			if name == "topic_id" && form.fields.Get(name) != "" {
				*hasTopic = true
			}
		}
		if name := attr(n, "name"); name == "native_captcha_token" {
			form.captchaToken = form.fields.Get(name)
		}
	}
	if n.Type == html.TextNode && strings.TrimSpace(n.Data) != "" {
		// 算术题通常是验证码控件旁的文本节点，向上汇总在表单扫描后处理。
	}
	for ch := n.FirstChild; ch != nil; ch = ch.NextSibling {
		collectReplyFields(ch, form, hasBody, hasTopic)
	}
}

func (c *Client) postReply(path, referer string, form url.Values) (int, []byte, string, error) {
	req, err := http.NewRequest(http.MethodPost, c.base+path, strings.NewReader(form.Encode()))
	if err != nil {
		return 0, nil, "", err
	}
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Referer", c.base+referer)
	resp, err := c.http.Do(req)
	if err != nil {
		return 0, nil, "", err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return resp.StatusCode, nil, resp.Request.URL.String(), err
	}
	return resp.StatusCode, body, resp.Request.URL.String(), nil
}

func isReplyFailure(message string) bool {
	for _, word := range []string{"失败", "错误", "无效", "验证码", "过短", "重复", "关闭", "不存在", "禁止", "未登录"} {
		if strings.Contains(message, word) {
			return true
		}
	}
	return false
}

func attr(n *html.Node, name string) string {
	for _, a := range n.Attr {
		if a.Key == name {
			return a.Val
		}
	}
	return ""
}
