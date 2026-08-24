// Package lottery 管理小号向指定抽奖帖回复的手动任务。
package lottery

import (
	"errors"
	"math/rand"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"gacha-buyer/internal/accounts"
	"gacha-buyer/internal/config"
	"gacha-buyer/internal/db"
	"gacha-buyer/internal/site"
)

// replier 抽象回复客户端，便于测试替换真实站点请求。
type replier interface {
	Reply(topicURL, message string) site.ReplyResult
	ReplyWithRetry(topicURL, message string) site.ReplyResult
}

// accountManager 抽象账号客户端获取，便于测试替换 Manager。
type accountManager interface {
	Sub(config.SubAccount) (replier, *accounts.Acct, error)
}

// engineDB 抽奖回复需要的数据库方法子集，便于测试替换。
type engineDB interface {
	LotteryReplyConfirmed(accountID, topicID int) bool
	LotteryReplyPendingRecently(accountID, topicID int, since time.Time) bool
	AddLotteryReply(r *db.LotteryReplyRow) error
	GetAccount(role, username string) (*db.AccountRow, error)
	ListLotteryReplies(limit int) ([]*db.LotteryReplyRow, error)
}

// Engine 抽奖回复执行器。它不自带定时器，只响应 Web 明确触发。
type Engine struct {
	cfg  *config.Config
	mgr  accountManager
	d    engineDB
	logf func(string, ...any)

	mu      sync.Mutex
	running bool
	stopCh  chan struct{}
	sleepFn func(time.Duration) bool
}

// ReplyLog 给 Web 展示的一条回复记录。
type ReplyLog struct {
	Time      time.Time `json:"time"`
	Sub       string    `json:"sub"`
	TopicID   int       `json:"topic_id"`
	Content   string    `json:"content"`
	Captcha   bool      `json:"captcha"`
	DryRun    bool      `json:"dry_run"`
	Submitted bool      `json:"submitted"`
	Confirmed bool      `json:"confirmed"`
	ReplyID   int       `json:"reply_id"`
	Message   string    `json:"message"`
}

// New 创建抽奖回复执行器。
func New(cfg *config.Config, mgr *accounts.Manager, d *db.DB, logf func(string, ...any)) *Engine {
	if logf == nil {
		logf = func(string, ...any) {}
	}
	eng := &Engine{cfg: cfg, d: d, logf: logf}
	if mgr != nil {
		eng.mgr = &managerAdapter{mgr: mgr}
	}
	return eng
}

type managerAdapter struct {
	mgr *accounts.Manager
}

func (m *managerAdapter) Sub(sub config.SubAccount) (replier, *accounts.Acct, error) {
	client, acct, err := m.mgr.Sub(sub)
	if err != nil {
		return nil, nil, err
	}
	return client, acct, nil
}

// NewWithMocks 创建可用于测试的 Engine。
func NewWithMocks(cfg *config.Config, mgr accountManager, d engineDB, logf func(string, ...any)) *Engine {
	eng := New(cfg, nil, nil, logf)
	eng.mgr = mgr
	eng.d = d
	eng.sleepFn = func(d time.Duration) bool {
		return true
	}
	return eng
}

// SetSleep 替换账号间等待，测试用。
func (e *Engine) SetSleep(fn func(time.Duration) bool) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.sleepFn = fn
}

// StartOnce 启动一轮任务；已有任务运行时返回 false。
func (e *Engine) StartOnce() bool {
	e.mu.Lock()
	if e.running {
		e.mu.Unlock()
		return false
	}
	e.running = true
	e.stopCh = make(chan struct{})
	stopCh := e.stopCh
	e.mu.Unlock()
	go func() {
		defer func() {
			e.mu.Lock()
			e.running = false
			e.mu.Unlock()
		}()
		e.runOnce(stopCh)
	}()
	return true
}

// Stop 请求停止等待中的任务。
func (e *Engine) Stop() {
	e.mu.Lock()
	ch := e.stopCh
	running := e.running
	e.mu.Unlock()
	if running && ch != nil {
		select {
		case <-ch:
		default:
			close(ch)
		}
	}
}

// Running 返回任务是否运行中。
func (e *Engine) Running() bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.running
}

// Logs 返回最近回复记录。
func (e *Engine) Logs(limit int) []ReplyLog {
	rows, err := e.d.ListLotteryReplies(limit)
	if err != nil {
		return []ReplyLog{}
	}
	out := make([]ReplyLog, 0, len(rows))
	for _, r := range rows {
		out = append(out, ReplyLog{Time: r.Time, Sub: r.Sub, TopicID: r.TopicID, Content: r.Content,
			Captcha: r.Captcha, DryRun: r.DryRun, Submitted: r.Submitted, Confirmed: r.Confirmed,
			ReplyID: r.ReplyID, Message: r.Message})
	}
	return out
}

func (e *Engine) runOnce(stopCh <-chan struct{}) {
	e.mu.Lock()
	cfg := *e.cfg
	e.mu.Unlock()
	cfg.Normalize()
	if cfg.Lottery.URL == "" {
		e.logf("[抽奖回复] 未配置抽奖帖 URL")
		return
	}
	topicID, err := topicID(cfg.Lottery.URL)
	if err != nil {
		e.logf("[抽奖回复] %v", err)
		return
	}
	messages := append([]string(nil), cfg.Lottery.Messages...)
	if len(messages) == 0 {
		e.logf("[抽奖回复] 语料库为空")
		return
	}
	rand.Shuffle(len(messages), func(i, j int) { messages[i], messages[j] = messages[j], messages[i] })
	messageAt := 0
	enabled := make([]config.SubAccount, 0, len(cfg.Subs))
	for _, sub := range cfg.Subs {
		if sub.Enabled && strings.TrimSpace(sub.Username) != "" {
			enabled = append(enabled, sub)
		}
	}
	for i, sub := range enabled {
		select {
		case <-stopCh:
			e.logf("[抽奖回复] 任务已停止")
			return
		default:
		}
		content := messages[messageAt%len(messages)]
		messageAt++
		res := e.replyOne(stopCh, cfg, sub, topicID, content)
		if i+1 < len(enabled) {
			cool := 65*time.Second + time.Duration(rand.Intn(45))*time.Second
			if res != nil && res.Retryable {
				cool = 90*time.Second + time.Duration(rand.Intn(60))*time.Second
			}
			if !e.sleep(stopCh, cool) {
				return
			}
		}
	}
}

func (e *Engine) replyOne(stopCh <-chan struct{}, cfg config.Config, sub config.SubAccount, topicID int, content string) *site.ReplyResult {
	row, err := e.d.GetAccount("sub", sub.Username)
	if err != nil && err != db.ErrNotFound {
		e.logf("[抽奖回复] %s 读取账号失败: %v", maskUser(sub.Username), err)
		return nil
	}
	accountID := 0
	if row != nil {
		accountID = row.ID
	}
	name := maskUser(sub.Username)
	if accountID > 0 && e.d.LotteryReplyConfirmed(accountID, topicID) {
		e.logf("[抽奖回复] %s 已确认回复过 topic/%d，跳过", name, topicID)
		return nil
	}
	if accountID > 0 && e.d.LotteryReplyPendingRecently(accountID, topicID, time.Now().Add(-24*time.Hour)) {
		e.logf("[抽奖回复] %s 最近已提交但未确认，跳过", name)
		return nil
	}
	log := &db.LotteryReplyRow{Time: time.Now(), AccountID: accountID, Sub: name, TopicID: topicID, Content: content, DryRun: cfg.DryRun}
	if cfg.DryRun {
		log.Message = "dry-run：仅记录，不提交回复"
		_ = e.d.AddLotteryReply(log)
		e.logf("[抽奖回复][DRY] %s → topic/%d：%s", name, topicID, content)
		return nil
	}
	select {
	case <-stopCh:
		return nil
	default:
	}
	client, acct, err := e.mgr.Sub(sub)
	if err != nil {
		log.Message = "登录失败: " + err.Error()
		_ = e.d.AddLotteryReply(log)
		e.logf("[抽奖回复] %s %s", name, log.Message)
		return nil
	}
	res := client.ReplyWithRetry(cfg.Lottery.URL, content)
	log.AccountID = acct.ID
	log.Captcha = res.NeedsCaptcha
	log.Submitted, log.Confirmed, log.ReplyID, log.Message = res.Submitted, res.Confirmed, res.ReplyID, res.Message
	if err := e.d.AddLotteryReply(log); err != nil {
		e.logf("[抽奖回复] %s 记录失败: %v", name, err)
	}
	e.logf("[抽奖回复] %s → topic/%d submitted=%v confirmed=%v %s", name, topicID, res.Submitted, res.Confirmed, res.Message)
	return &res
}

func topicID(raw string) (int, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return 0, errors.New("抽奖帖 URL 不合法")
	}
	parts := strings.Split(strings.Trim(u.Path, "/"), "/")
	if len(parts) != 2 || parts[0] != "topic" {
		return 0, errors.New("抽奖帖 URL 必须是 /topic/<id>")
	}
	n, err := strconv.Atoi(parts[1])
	if err != nil || n <= 0 {
		return 0, errors.New("抽奖帖 ID 不合法")
	}
	return n, nil
}

func (e *Engine) sleep(stopCh <-chan struct{}, d time.Duration) bool {
	e.mu.Lock()
	fn := e.sleepFn
	e.mu.Unlock()
	if fn != nil {
		return fn(d)
	}
	return sleepInterruptible(stopCh, d)
}

func sleepInterruptible(stopCh <-chan struct{}, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-stopCh:
		return false
	case <-t.C:
		return true
	}
}

func maskUser(u string) string {
	at := strings.IndexByte(u, '@')
	name, domain := u, ""
	if at >= 0 {
		name, domain = u[:at], u[at:]
	}
	r := []rune(name)
	if len(r) <= 2 {
		return name + "***" + domain
	}
	return string(r[:2]) + "***" + domain
}
