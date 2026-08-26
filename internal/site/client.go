// Package site 封装与 linux.sb 的 HTTP 交互：登录、会话保活、市场抓取与购买。
package site

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"golang.org/x/net/html"

	"gacha-buyer/internal/config"
)

const userAgent = "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/126.0.0.0 Safari/537.36"

// Client 是绑定单个账号的站点客户端，cookie 自动随请求携带。
type Client struct {
	base     string
	http     *http.Client
	cfg      *config.Config
	username string
	password string
	jar      http.CookieJar

	logf func(format string, args ...any)
}

// NewClient creates a client using credentials in cfg (legacy standalone mode).
func NewClient(cfg *config.Config, logf func(string, ...any)) (*Client, error) {
	return NewClientFor(cfg, cfg.Username, cfg.Password, logf)
}

// NewClientFor creates a client bound to one account's credentials.
func NewClientFor(cfg *config.Config, username, password string, logf func(string, ...any)) (*Client, error) {
	if logf == nil {
		logf = func(string, ...any) {}
	}
	jar, err := cookiejar.New(nil)
	if err != nil {
		return nil, err
	}
	c := &Client{
		base:     cfg.Site,
		cfg:      cfg,
		username: username,
		password: password,
		jar:      jar,
		logf:     logf,
		http: &http.Client{
			Timeout: 20 * time.Second,
			Jar:     jar,
			CheckRedirect: func(req *http.Request, via []*http.Request) error {
				if len(via) >= 5 {
					return errors.New("重定向次数过多")
				}
				return nil
			},
		},
	}
	return c, nil
}

// ExportCookies 导出会话 cookie（持久化用）。
func (c *Client) ExportCookies() []*http.Cookie {
	u, _ := url.Parse(c.base)
	return c.jar.Cookies(u)
}

// ImportCookies 恢复会话 cookie。
func (c *Client) ImportCookies(cookies []*http.Cookie) {
	if len(cookies) == 0 {
		return
	}
	u, _ := url.Parse(c.base)
	c.jar.SetCookies(u, cookies)
}

// get 请求页面并返回响应体（自动带 UA 与 Referer）。
func (c *Client) get(path string) (int, []byte, error) {
	req, err := http.NewRequest(http.MethodGet, c.base+path, nil)
	if err != nil {
		return 0, nil, err
	}
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8")
	resp, err := c.http.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	return resp.StatusCode, body, err
}

// RawGet 供外部探测用的 GET（跟随 cookie 会话）。
func (c *Client) RawGet(path string) (int, []byte, error) { return c.get(path) }

// IsLoggedIn 用一次轻量首页请求判断当前 cookie 是否仍处于登录态。
func (c *Client) IsLoggedIn() bool {
	status, body, err := c.get("/")
	if err != nil || status != http.StatusOK {
		return false
	}
	page := string(body)
	// 未登录页无"我的积分"入口，且登录表单会出现
	return reSideUID.MatchString(page) && !strings.Contains(page, `name="password"`)
}

// postForm 提交表单（Referer 设为目标路径，兼容旧调用）。
func (c *Client) postForm(path string, form url.Values) (int, []byte, error) {
	return c.postFormRef(path, path, form)
}

// postFormRef 提交表单，Referer 设为 referer 参数指定的页面路径。
func (c *Client) postFormRef(path, referer string, form url.Values) (int, []byte, error) {
	req, err := http.NewRequest(http.MethodPost, c.base+path, strings.NewReader(form.Encode()))
	if err != nil {
		return 0, nil, err
	}
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Referer", c.base+referer)
	resp, err := c.http.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	return resp.StatusCode, body, err
}

// ---------------------------------------------------------------- 登录 ----

// loginForm 从 /login 页面提取的表单要素。
type loginForm struct {
	csrf       string
	captchaQ   string // 展示用算术题
	captchaTok string
	powPrefix  string
	powZeroes  int
	trapName   string // 蜜罐字段名（提交时留空）
	extra      map[string]string
}

var (
	reCSRF     = regexp.MustCompile(`name="_csrf"\s+value="([0-9a-f]{64})"`)
	reCapToken = regexp.MustCompile(`name="native_captcha_token"\s+value="([^"]+)"`)
)

// fetchLoginForm 抓取并解析登录页。
func (c *Client) fetchLoginForm() (*loginForm, error) {
	status, body, err := c.get("/login")
	if err != nil {
		return nil, fmt.Errorf("访问登录页失败: %w", err)
	}
	page := string(body)
	if status != http.StatusOK {
		return nil, fmt.Errorf("登录页返回 HTTP %d", status)
	}
	f := &loginForm{extra: map[string]string{}}
	if m := reCSRF.FindStringSubmatch(page); m != nil {
		f.csrf = m[1]
	}
	if m := reCapToken.FindStringSubmatch(page); m != nil {
		f.captchaTok = m[1]
	}
	if f.csrf == "" || f.captchaTok == "" {
		return nil, errors.New("登录页缺少 _csrf 或验证码令牌")
	}

	// 解码 captcha token（JWT 形如 payload.base64url），提取题目与 PoW 挑战。
	payload := tokenPayload(f.captchaTok)
	qRaw := jsonStr(payload, "question")
	f.captchaQ = qRaw
	f.powPrefix = jsonStr(payload, "pow")
	zeros := jsonInt(payload, "zeros")
	f.powZeroes = zeros

	// 兜底：从页面可见文本中找算术题。
	if f.captchaQ == "" {
		if text := extractVisible(page); text != "" {
			for _, line := range strings.Split(text, "\n") {
				line = strings.TrimSpace(line)
				if strings.HasSuffix(line, "= ?") || strings.HasSuffix(line, "＝ ？") {
					f.captchaQ = line
					break
				}
			}
		}
	}
	if f.captchaQ == "" {
		return nil, errors.New("未找到验证码算术题")
	}

	// 蜜罐字段：class="native-captcha-trap" 内的 input name。
	f.trapName = findTrapName(page)
	return f, nil
}

// tokenPayload 提取站点令牌的 base64url 段。站点为两段式 payload.signature，
// payload 在第一段；兼容标准 JWT 三段式（payload 在第二段）。
func tokenPayload(tok string) string {
	parts := strings.Split(tok, ".")
	var seg string
	switch len(parts) {
	case 2:
		seg = parts[0]
	case 3:
		seg = parts[1]
	default:
		return ""
	}
	raw, err := base64.RawURLEncoding.DecodeString(seg)
	if err != nil {
		return ""
	}
	return string(raw)
}

// jsonStr 从扁平 JSON 字符串里取字符串值（避免引入 encoding/json 二次解码——payload 是 JSON，直接用标准库也行）。
func jsonStr(payload, key string) string {
	re := regexp.MustCompile(`"` + key + `":"([^"]*)"`)
	m := re.FindStringSubmatch(payload)
	if m == nil {
		return ""
	}
	return m[1]
}

// jsonInt 取整数值。
func jsonInt(payload, key string) int {
	re := regexp.MustCompile(`"` + key + `":(\d+)`)
	m := re.FindStringSubmatch(payload)
	if m == nil {
		return 0
	}
	n, _ := strconv.Atoi(m[1])
	return n
}

// findTrapName 找蜜罐 input 的 name。
func findTrapName(page string) string {
	idx := strings.Index(page, "native-captcha-trap")
	if idx < 0 {
		return ""
	}
	seg := page[idx:]
	m := regexp.MustCompile(`name="([^"]+)"`).FindStringSubmatch(seg)
	if m == nil {
		return ""
	}
	return m[1]
}

// extractVisible 粗略剥离标签取可见文本（兜底找题目用）。
func extractVisible(page string) string {
	doc, err := html.Parse(strings.NewReader(page))
	if err != nil {
		return ""
	}
	var sb strings.Builder
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.TextNode && n.Data != "" {
			sb.WriteString(n.Data)
			sb.WriteString("\n")
		}
		for ch := n.FirstChild; ch != nil; ch = ch.NextSibling {
			if n.Type == html.ElementNode && (n.Data == "script" || n.Data == "style") {
				continue
			}
			walk(ch)
		}
	}
	walk(doc)
	return sb.String()
}

// SolveCaptcha 求解算术题，支持 + - × x X * ÷ / 与全角符号。
func SolveCaptcha(q string) (string, error) {
	q = strings.Map(func(r rune) rune {
		switch r {
		case '＋':
			return '+'
		case '－', '—', '–':
			return '-'
		case '＝':
			return '='
		case '？':
			return '?'
		}
		return r
	}, q)
	q = strings.NewReplacer(" ", "", "\t", "", "\n", "").Replace(q)
	re := regexp.MustCompile(`^(\d+)([+\-×xX*/÷])(\d+)=\??$`)
	m := re.FindStringSubmatch(q)
	if m == nil {
		return "", fmt.Errorf("无法解析算术题: %q", q)
	}
	a, _ := strconv.Atoi(m[1])
	b, _ := strconv.Atoi(m[3])
	switch m[2] {
	case "+":
		return strconv.Itoa(a + b), nil
	case "-":
		return strconv.Itoa(a - b), nil
	case "×", "x", "X", "*":
		return strconv.Itoa(a * b), nil
	case "÷", "/":
		if b == 0 {
			return "", errors.New("除数为零")
		}
		if a%b != 0 {
			return "", fmt.Errorf("非整除: %d/%d", a, b)
		}
		return strconv.Itoa(a / b), nil
	}
	return "", errors.New("未知运算符")
}

// SolvePoW 计算 sha256(prefix:hexnonce) 前导 zeroes 个十六进制零的 nonce。
func SolvePoW(prefix string, zeroes int) (string, error) {
	if zeroes <= 0 || zeroes > 8 {
		return "", fmt.Errorf("非法 PoW 难度: %d", zeroes)
	}
	target := strings.Repeat("0", zeroes)
	for i := uint64(0); i < 8_000_000; i++ {
		nonce := strconv.FormatUint(i, 16)
		sum := sha256.Sum256([]byte(prefix + ":" + nonce))
		if hex.EncodeToString(sum[:])[:zeroes] == target {
			return nonce, nil
		}
	}
	return "", errors.New("PoW 求解失败：超过尝试上限")
}

// Login 执行完整登录流程；成功后 cookie 保存在 client 内。
func (c *Client) Login() error {
	form, err := c.fetchLoginForm()
	if err != nil {
		return err
	}
	answer, err := SolveCaptcha(form.captchaQ)
	if err != nil {
		return fmt.Errorf("验证码求解失败: %w", err)
	}
	c.logf("登录验证码: %s = %s", form.captchaQ, answer)

	// 拟人延迟：真人答题+PoW 需要几秒，秒提交会触发"提交过快"风控
	delay := time.Duration(2500+rand.Intn(2500)) * time.Millisecond
	c.logf("等待 %v 后提交登录…", delay.Round(time.Millisecond))
	time.Sleep(delay)

	payload := url.Values{}
	payload.Set("_csrf", form.csrf)
	payload.Set("username", c.username)
	payload.Set("password", c.password)
	payload.Set("native_captcha_answer", answer)
	payload.Set("native_captcha_token", form.captchaTok)
	if form.powPrefix != "" && form.powZeroes > 0 {
		nonce, err := SolvePoW(form.powPrefix, form.powZeroes)
		if err != nil {
			return fmt.Errorf("PoW 求解失败: %w", err)
		}
		c.logf("PoW: prefix=%s zeros=%d nonce=%s", form.powPrefix, form.powZeroes, nonce)
		payload.Set("native_captcha_pow", nonce)
	} else {
		payload.Set("native_captcha_pow", "")
	}
	if form.trapName != "" {
		payload.Set(form.trapName, "") // 蜜罐必须为空
	}
	for k, v := range form.extra {
		payload.Set(k, v)
	}

	status, body, err := c.postForm("/login", payload)
	if err != nil {
		return fmt.Errorf("提交登录失败: %w", err)
	}
	page := string(body)
	// 失败时站点回 200 + 错误文案或 302 回 /login；成功通常 302 跳首页或回 200 首页内容。
	if status >= 400 {
		return fmt.Errorf("登录返回 HTTP %d", status)
	}
	ok, reason := looksLoggedIn(page, status)
	if !ok {
		if msg := extractAlert(page); msg != "" {
			return fmt.Errorf("登录被拒绝: %s", msg)
		}
		return fmt.Errorf("登录未生效（HTTP %d）%s", status, reason)
	}
	c.logf("登录成功: %s", c.username)
	return nil
}

// looksLoggedIn 判断页面是否处于登录态。
func looksLoggedIn(page string, status int) (bool, string) {
	low := strings.ToLower(page)
	if strings.Contains(low, "退出登录") || strings.Contains(low, "/logout") ||
		strings.Contains(low, "个人设置") {
		return true, ""
	}
	return false, fmt.Sprintf("(body=%d bytes)", len(page))
}

// extractAlert 提取页面错误提示。
func extractAlert(page string) string {
	re := regexp.MustCompile(`(?is)<div[^>]*class="[^"]*(alert|error|notice|toast)[^"]*"[^>]*>(.*?)</div>`)
	m := re.FindStringSubmatch(page)
	if m == nil {
		return ""
	}
	text := extractVisible(m[2])
	return strings.TrimSpace(strings.Join(strings.Fields(text), " "))
}

// EnsureLoggedIn 会话失效时重新登录。
func (c *Client) EnsureLoggedIn() error {
	status, _, err := c.get("/")
	if err != nil {
		return err
	}
	if status == http.StatusOK {
		// 首页 200 不代表已登录（匿名也能看首页），需检查内容。
		return nil
	}
	return c.Login()
}
