// Package collector 每日归集：小号签到后把积分通过打赏主号帖子转给主号。
package collector

import (
	"errors"
	"fmt"
	"math/rand"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"gacha-buyer/internal/accounts"
	"gacha-buyer/internal/config"
	"gacha-buyer/internal/db"
	"gacha-buyer/internal/site"
)

const (
	donateMax = 99 // 站点单次打赏限额
)

var reTopicID = regexp.MustCompile(`href="/topic/(\d+)"`)

// Engine 归集调度器。
type Engine struct {
	cfg  *config.Config
	mgr  *accounts.Manager
	d    *db.DB
	logf func(string, ...any)

	mu      sync.Mutex
	lastRun time.Time
	nextRun time.Time
	running bool
	stopCh  chan struct{}
	done    chan struct{}
}

// TransferLog 一条小号归集记录。
type TransferLog struct {
	AccountID int       `json:"-"`
	Time      time.Time `json:"time"`
	Sub       string    `json:"sub"`
	CheckIn   bool      `json:"check_in"`
	Balance   int       `json:"balance"`
	TipAmount int       `json:"tip_amount"`
	TopicID   int       `json:"topic_id"`
	DryRun    bool      `json:"dry_run"`
	OK        bool      `json:"ok"`
	Message   string    `json:"message"`
}

// New 创建归集引擎。
func New(cfg *config.Config, mgr *accounts.Manager, d *db.DB, logf func(string, ...any)) *Engine {
	if logf == nil {
		logf = func(string, ...any) {}
	}
	e := &Engine{cfg: cfg, mgr: mgr, d: d, logf: logf, stopCh: make(chan struct{}), done: make(chan struct{})}
	e.nextRun = e.calcNext()
	return e
}

func (e *Engine) calcNext() time.Time {
	now := time.Now()
	next := time.Date(now.Year(), now.Month(), now.Day(), e.cfg.Collector.AtHour, 0, 0, 0, now.Location())
	if !next.After(now) {
		next = next.Add(24 * time.Hour)
	}
	return next
}

// Start 启动每日调度循环。
func (e *Engine) Start() {
	e.mu.Lock()
	if e.running {
		e.mu.Unlock()
		return
	}
	e.running = true
	e.stopCh = make(chan struct{})
	e.done = make(chan struct{})
	e.mu.Unlock()
	go e.loop()
	e.logf("归集引擎已启动，下次执行 %s", e.nextRun.Format("2006-01-02 15:04"))
}

// Stop 停止调度。
func (e *Engine) Stop() {
	e.mu.Lock()
	if !e.running {
		e.mu.Unlock()
		return
	}
	e.running = false
	ch := e.stopCh
	d := e.done
	e.mu.Unlock()
	close(ch)
	select {
	case <-d:
	case <-time.After(5 * time.Second):
	}
}

func (e *Engine) loop() {
	defer close(e.done)
	for {
		e.mu.Lock()
		next := e.nextRun
		e.mu.Unlock()
		wait := time.Until(next)
		if wait < 0 {
			wait = 0
		}
		timer := time.NewTimer(wait)
		select {
		case <-e.stopCh:
			timer.Stop()
			return
		case <-timer.C:
		}
		e.RunOnce(false)
		e.mu.Lock()
		e.lastRun = time.Now()
		e.nextRun = e.calcNext()
		e.mu.Unlock()
	}
}

// RunOnce 立即执行一轮归集。manual=true 时忽略当日已执行检查。
func (e *Engine) RunOnce(manual bool) []TransferLog {
	var results []TransferLog
	col := e.cfg.Collector
	_ = col
	for _, s := range e.cfg.Subs {
		if !s.Enabled {
			continue
		}
		select {
		case <-e.stopCh:
			return results
		default:
		}
		results = append(results, e.collectOne(s, manual))
		// 账号间隔随机 20~60s，避免并发特征
		time.Sleep(time.Duration(20+rand.Intn(40)) * time.Second)
	}
	e.saveLogs(results)
	return results
}

// collectOne 单个小号：登录→签到→查余额→随机主题→打赏。
func (e *Engine) collectOne(s config.SubAccount, manual bool) TransferLog {
	tl := TransferLog{Time: time.Now(), Sub: maskUser(s.Username), DryRun: e.cfg.CollectorDryRun()}
	c, a, err := e.mgr.Sub(s)
	if err != nil {
		tl.Message = err.Error()
		e.logf("[归集] %s 登录失败: %v", tl.Sub, err)
		return tl
	}
	tl.AccountID = a.ID
	if err := c.CheckIn(); err != nil {
		tl.Message = err.Error()
		return tl
	}
	tl.CheckIn = true

	// 即使手工触发也遵守每个小号每日最多打赏一次。
	_ = manual
	if !e.cfg.CollectorDryRun() && e.d.TippedToday(tl.AccountID, time.Now()) {
		tl.OK = true
		tl.Message = "今日已归集，跳过"
		return tl
	}

	balance, err := c.FetchPoints()
	if err != nil {
		tl.Message = "查询余额失败: " + err.Error()
		return tl
	}
	tl.Balance = balance

	tip := balance - e.cfg.Collector.Keep
	if tip < e.cfg.Collector.MinTip {
		tl.OK = true
		tl.Message = fmt.Sprintf("余额 %d 不足以归集（保留 %d）", balance, e.cfg.Collector.Keep)
		return tl
	}
	if tip > donateMax {
		tip = donateMax // 站点单次限额 99；超出部分次日继续
	}

	topicID, err := e.pickMainTopic(c)
	if err != nil {
		tl.Message = "找主号帖子失败: " + err.Error()
		return tl
	}
	tl.TopicID = topicID

	if e.cfg.CollectorDryRun() {
		tl.TipAmount = tip
		tl.OK = true
		tl.Message = fmt.Sprintf("dry-run：将打赏 %d 积分到 topic/%d", tip, topicID)
		e.logf("[归集][DRY] %s → 打赏 %s %d 分 (topic/%d)", tl.Sub, "主号", tip, topicID)
		return tl
	}

	res := c.Donate(topicID, tip, e.cfg.Collector.Message)
	tl.TipAmount = tip
	tl.OK = res.OK
	tl.Message = res.Message
	e.logf("[归集] %s → topic/%d 打赏 %d 分: ok=%v %s", tl.Sub, topicID, tip, res.OK, res.Message)
	_ = a
	return tl
}

// pickMainTopic 随机挑一篇主号发的帖子。
// 协议：/user/<uid>?tab=topics 列出主号主题；从列表页提取 topic id 随机选一个；
// 若配置了固定 topic 且列表失败则回退到配置值。
func (e *Engine) pickMainTopic(sub *site.Client) (int, error) {
	if e.cfg.Collector.TopicID > 0 {
		return e.cfg.Collector.TopicID, nil
	}
	mainClient, mainAcct, err := e.mgr.Main()
	if err != nil {
		return 0, fmt.Errorf("主号会话不可用: %w", err)
	}
	if mainAcct.UID == 0 {
		uid, uerr := mainClient.GetMyUID()
		if uerr != nil {
			return 0, fmt.Errorf("取主号 UID 失败: %w", uerr)
		}
		e.mgr.SetUID("main", e.cfg.Username, uid)
		mainAcct.UID = uid
	}
	status, body, err := sub.RawGet(fmt.Sprintf("/user/%d?tab=topics", mainAcct.UID))
	if err != nil || status != 200 {
		return 0, fmt.Errorf("主号主题页 HTTP %d", status)
	}
	ids := reTopicID.FindAllStringSubmatch(string(body), -1)
	seen := map[string]bool{}
	var list []int
	for _, m := range ids {
		if !seen[m[1]] {
			seen[m[1]] = true
			n, _ := strconv.Atoi(m[1])
			list = append(list, n)
		}
	}
	if len(list) == 0 {
		if e.cfg.Collector.TopicID > 0 {
			return e.cfg.Collector.TopicID, nil
		}
		return 0, errors.New("主号没有可见主题")
	}
	pick := list[rand.Intn(len(list))]
	return pick, nil
}

// tippedToday 已由 db.TippedToday 承担（按掩码小号名查最近成功记录）。

// ---- 记录持久化（SQLite transfers 表）----

// saveLogs 归集记录落库。
func (e *Engine) saveLogs(logs []TransferLog) {
	for i := range logs {
		l := logs[i]
		_ = e.d.AddTransfer(&db.TransferRow{
			Time: l.Time, AccountID: l.AccountID, Sub: l.Sub, CheckIn: l.CheckIn, Balance: l.Balance,
			TipAmount: l.TipAmount, TopicID: l.TopicID, DryRun: l.DryRun,
			OK: l.OK, Message: l.Message,
		})
	}
}

// Transfers 给前端的记录（新→旧）。
func (e *Engine) Transfers() []TransferLog {
	rows, err := e.d.ListTransfers(500)
	if err != nil {
		return []TransferLog{}
	}
	out := make([]TransferLog, len(rows))
	for i, r := range rows {
		out[i] = TransferLog{
			AccountID: r.AccountID, Time: r.Time, Sub: r.Sub, CheckIn: r.CheckIn, Balance: r.Balance,
			TipAmount: r.TipAmount, TopicID: r.TopicID, DryRun: r.DryRun,
			OK: r.OK, Message: r.Message,
		}
	}
	return out
}

// Status 给前端展示。
type Status struct {
	Running bool   `json:"running"`
	NextRun string `json:"next_run,omitempty"`
	LastRun string `json:"last_run,omitempty"`
	TopicID int    `json:"topic_id"`
	Keep    int    `json:"keep"`
	AtHour  int    `json:"at_hour"`
}

func (e *Engine) Snapshot() Status {
	e.mu.Lock()
	defer e.mu.Unlock()
	s := Status{
		Running: e.running,
		TopicID: e.cfg.Collector.TopicID,
		Keep:    e.cfg.Collector.Keep,
		AtHour:  e.cfg.Collector.AtHour,
	}
	if !e.nextRun.IsZero() && e.running {
		s.NextRun = e.nextRun.Format("2006-01-02 15:04")
	}
	if !e.lastRun.IsZero() {
		s.LastRun = e.lastRun.Format("2006-01-02 15:04")
	}
	return s
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
