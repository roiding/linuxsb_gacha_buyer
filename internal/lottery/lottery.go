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
)

// Engine 抽奖回复执行器。它不自带定时器，只响应 Web 明确触发。
type Engine struct {
	cfg  *config.Config
	mgr  *accounts.Manager
	d    *db.DB
	logf func(string, ...any)

	mu      sync.Mutex
	running bool
	stopCh  chan struct{}
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
	return &Engine{cfg: cfg, mgr: mgr, d: d, logf: logf}
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
		e.replyOne(stopCh, cfg, sub, topicID, content)
		if i+1 < len(enabled) && !sleepInterruptible(stopCh, 20*time.Second+time.Duration(rand.Intn(40))*time.Second) {
			return
		}
	}
}

func (e *Engine) replyOne(stopCh <-chan struct{}, cfg config.Config, sub config.SubAccount, topicID int, content string) {
	row, err := e.d.GetAccount("sub", sub.Username)
	if err != nil && err != db.ErrNotFound {
		e.logf("[抽奖回复] %s 读取账号失败: %v", maskUser(sub.Username), err)
		return
	}
	accountID := 0
	if row != nil {
		accountID = row.ID
	}
	name := maskUser(sub.Username)
	if accountID > 0 && e.d.LotteryReplyConfirmed(accountID, topicID) {
		e.logf("[抽奖回复] %s 已确认回复过 topic/%d，跳过", name, topicID)
		return
	}
	if accountID > 0 && e.d.LotteryReplyPendingRecently(accountID, topicID, time.Now().Add(-24*time.Hour)) {
		e.logf("[抽奖回复] %s 最近已提交但未确认，跳过", name)
		return
	}
	log := &db.LotteryReplyRow{Time: time.Now(), AccountID: accountID, Sub: name, TopicID: topicID, Content: content, DryRun: cfg.DryRun}
	if cfg.DryRun {
		log.Message = "dry-run：仅记录，不提交回复"
		_ = e.d.AddLotteryReply(log)
		e.logf("[抽奖回复][DRY] %s → topic/%d：%s", name, topicID, content)
		return
	}
	select {
	case <-stopCh:
		return
	default:
	}
	client, acct, err := e.mgr.Sub(sub)
	if err != nil {
		log.Message = "登录失败: " + err.Error()
		_ = e.d.AddLotteryReply(log)
		e.logf("[抽奖回复] %s %s", name, log.Message)
		return
	}
	res := client.Reply(cfg.Lottery.URL, content)
	log.AccountID = acct.ID
	log.Captcha = res.NeedsCaptcha
	log.Submitted, log.Confirmed, log.ReplyID, log.Message = res.Submitted, res.Confirmed, res.ReplyID, res.Message
	if err := e.d.AddLotteryReply(log); err != nil {
		e.logf("[抽奖回复] %s 记录失败: %v", name, err)
	}
	e.logf("[抽奖回复] %s → topic/%d submitted=%v confirmed=%v %s", name, topicID, res.Submitted, res.Confirmed, res.Message)
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
