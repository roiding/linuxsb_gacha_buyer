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

const donateMax = 99

var (
	reTopicID            = regexp.MustCompile(`href="/topic/(\d+)"`)
	collectorAccountWait = func() time.Duration { return time.Duration(20+rand.Intn(40)) * time.Second }
	collectorRetryWait   = func(attempt int) time.Duration { return time.Duration(attempt*3) * time.Second }
)

// Engine 管理每日随机计划和串行归集任务。
type Engine struct {
	cfg  *config.Config
	mgr  *accounts.Manager
	d    *db.DB
	logf func(string, ...any)

	mu         sync.Mutex
	lastRun    time.Time
	nextRun    time.Time
	running    bool
	executing  bool
	stopCh     chan struct{}
	done       chan struct{}
	reschedule chan struct{}
	runMu      sync.Mutex
}

// TransferLog 是一条小号归集记录。
type TransferLog struct {
	AccountID     int       `json:"-"`
	Time          time.Time `json:"time"`
	Sub           string    `json:"sub"`
	CheckIn       bool      `json:"check_in"`
	Balance       int       `json:"balance"`
	BalanceBefore int       `json:"balance_before"`
	BalanceAfter  int       `json:"balance_after"`
	TipAmount     int       `json:"tip_amount"`
	TopicID       int       `json:"topic_id"`
	DryRun        bool      `json:"dry_run"`
	OK            bool      `json:"ok"`
	Submitted     bool      `json:"submitted"`
	Confirmed     bool      `json:"confirmed"`
	Pending       bool      `json:"pending"`
	Retryable     bool      `json:"retryable"`
	HTTP          int       `json:"http"`
	Attempt       int       `json:"attempt"`
	Message       string    `json:"message"`
	RecordID      int64     `json:"-"`
}

// New 创建归集引擎。
func New(cfg *config.Config, mgr *accounts.Manager, d *db.DB, logf func(string, ...any)) *Engine {
	if logf == nil {
		logf = func(string, ...any) {}
	}
	return &Engine{
		cfg: cfg, mgr: mgr, d: d, logf: logf,
		stopCh: make(chan struct{}), done: make(chan struct{}), reschedule: make(chan struct{}, 1),
	}
}

// Start 启动每日调度。当天计划已过时会立即补执行。
func (e *Engine) Start() {
	e.mu.Lock()
	if e.running {
		e.mu.Unlock()
		return
	}
	e.running = true
	e.stopCh = make(chan struct{})
	e.done = make(chan struct{})
	e.nextRun = e.calcNext(time.Now())
	e.mu.Unlock()
	go e.loop()
	e.logf("归集引擎已启动，下次执行 %s", e.nextRun.Format("2006-01-02 15:04"))
}

// Stop 停止调度和可中断等待。
func (e *Engine) Stop() {
	e.mu.Lock()
	if !e.running {
		e.mu.Unlock()
		return
	}
	e.running = false
	stopCh, done := e.stopCh, e.done
	e.mu.Unlock()
	close(stopCh)
	select {
	case <-done:
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
		case <-e.reschedule:
			timer.Stop()
			e.mu.Lock()
			e.nextRun = e.calcNext(time.Now())
			e.mu.Unlock()
			continue
		case <-timer.C:
		}
		if !e.runOnceLockedAvailable() {
			e.mu.Lock()
			e.nextRun = time.Now().Add(15 * time.Minute)
			e.mu.Unlock()
			continue
		}
		e.mu.Lock()
		e.lastRun = time.Now()
		e.nextRun = e.calcNext(time.Now())
		e.mu.Unlock()
	}
}

// Reschedule 让配置变更立即重算尚未执行的随机计划。
func (e *Engine) Reschedule() {
	now := time.Now()
	if schedule, err := e.d.GetCollectorSchedule(now.Format("2006-01-02")); err == nil && schedule.Status == "planned" {
		_ = e.d.DeleteCollectorSchedule(schedule.Day)
	}
	select {
	case e.reschedule <- struct{}{}:
	default:
	}
}

func (e *Engine) calcNext(now time.Time) time.Time {
	day := now.Local().Format("2006-01-02")
	if schedule, err := e.d.GetCollectorSchedule(day); err == nil {
		if schedule.Status != "completed" {
			if schedule.PlannedAt.After(now) {
				return schedule.PlannedAt.Local()
			}
			return now
		}
		return e.ensurePlan(now.AddDate(0, 0, 1), now)
	}
	return e.ensurePlan(now, now)
}

func (e *Engine) ensurePlan(dayTime, now time.Time) time.Time {
	dayTime = dayTime.Local()
	day := dayTime.Format("2006-01-02")
	if schedule, err := e.d.GetCollectorSchedule(day); err == nil {
		return schedule.PlannedAt.Local()
	}
	cfg := e.cfg.Collector
	windowStart := time.Date(dayTime.Year(), dayTime.Month(), dayTime.Day(), cfg.AtHour, 0, 0, 0, dayTime.Location())
	windowEnd := windowStart.Add(time.Duration(cfg.RandomWindowMin) * time.Minute)
	from := windowStart
	if day == now.Local().Format("2006-01-02") && now.After(from) {
		from = now
	}
	planned := from
	if from.Before(windowEnd) {
		minutes := int(windowEnd.Sub(from) / time.Minute)
		if minutes > 0 {
			planned = from.Add(time.Duration(rand.Intn(minutes+1)) * time.Minute)
		}
	}
	if planned.Before(now) {
		planned = now
	}
	schedule := &db.CollectorSchedule{Day: day, PlannedAt: planned, Status: "planned"}
	if err := e.d.SaveCollectorSchedule(schedule); err != nil {
		e.logf("保存归集计划失败: %v", err)
	}
	return planned
}

func (e *Engine) runOnceLockedAvailable() bool {
	if !e.runMu.TryLock() {
		return false
	}
	defer e.runMu.Unlock()
	e.setExecuting(true)
	defer e.setExecuting(false)
	e.runOnceLocked(false)
	return true
}

// StartOnce 异步启动手工归集；运行中返回 false。
func (e *Engine) StartOnce() bool {
	if !e.runMu.TryLock() {
		return false
	}
	e.setExecuting(true)
	go func() {
		defer e.runMu.Unlock()
		defer e.setExecuting(false)
		e.runOnceLocked(true)
	}()
	return true
}

// RunOnce 同步执行一轮；手工执行也遵守确认和每日去重。
func (e *Engine) RunOnce(manual bool) []TransferLog {
	if !e.runMu.TryLock() {
		return []TransferLog{{Time: time.Now(), Message: "归集任务已在运行"}}
	}
	defer e.runMu.Unlock()
	e.setExecuting(true)
	defer e.setExecuting(false)
	return e.runOnceLocked(manual)
}

func (e *Engine) runOnceLocked(manual bool) []TransferLog {
	_ = manual
	cfg := *e.cfg
	cfg.Subs = append([]config.SubAccount(nil), e.cfg.Subs...)
	now := time.Now()
	schedule := e.scheduleForRun(now)
	var enabled []config.SubAccount
	for _, sub := range cfg.Subs {
		if sub.Enabled {
			enabled = append(enabled, sub)
		}
	}
	results := make([]TransferLog, 0, len(enabled))
	interrupted := false
	for i, sub := range enabled {
		select {
		case <-e.stopCh:
			interrupted = true
		default:
		}
		if interrupted {
			break
		}
		log := e.collectOne(cfg, sub)
		results = append(results, log)
		if log.RecordID == 0 {
			if err := e.saveLog(log); err != nil {
				e.logf("[归集] %s 记录保存失败: %v", log.Sub, err)
			}
		}
		if i < len(enabled)-1 && !e.waitStop(collectorAccountWait()) {
			interrupted = true
			break
		}
	}
	e.finishSchedule(schedule, results, len(enabled), interrupted)
	return results
}

func (e *Engine) scheduleForRun(now time.Time) *db.CollectorSchedule {
	day := now.Local().Format("2006-01-02")
	schedule, err := e.d.GetCollectorSchedule(day)
	if err != nil {
		planned := e.ensurePlan(now, now)
		schedule = &db.CollectorSchedule{Day: day, PlannedAt: planned, Status: "planned"}
	}
	schedule.Status = "running"
	if schedule.StartedAt.IsZero() {
		schedule.StartedAt = now
	}
	_ = e.d.SaveCollectorSchedule(schedule)
	return schedule
}

func (e *Engine) finishSchedule(schedule *db.CollectorSchedule, results []TransferLog, enabled int, interrupted bool) {
	now := time.Now()
	needsVerification := false
	for _, result := range results {
		if result.Pending || (!result.OK && result.TipAmount > 0 && result.Retryable) {
			needsVerification = true
			break
		}
	}
	if interrupted || len(results) < enabled || needsVerification {
		schedule.Status = "retry"
		schedule.PlannedAt = now.Add(15 * time.Minute)
		schedule.CompletedAt = time.Time{}
	} else {
		schedule.Status = "completed"
		schedule.CompletedAt = now
	}
	if err := e.d.SaveCollectorSchedule(schedule); err != nil {
		e.logf("更新归集计划状态失败: %v", err)
	}
}

func (e *Engine) collectOne(cfg config.Config, sub config.SubAccount) TransferLog {
	log := TransferLog{Time: time.Now(), Sub: maskUser(sub.Username), DryRun: cfg.DryRun, Retryable: true}
	client, account, err := e.mgr.Sub(sub)
	if err != nil {
		log.Message = err.Error()
		return log
	}
	log.AccountID = account.ID
	if err := client.CheckIn(); err != nil {
		log.Message = err.Error()
		return log
	}
	log.CheckIn = true
	if !cfg.DryRun && e.d.TransferHandledToday(log.AccountID, time.Now()) {
		log.OK, log.Confirmed = true, true
		log.Message = "今日已处理，跳过"
		return log
	}
	if !cfg.DryRun {
		if resolved, stop := e.resolvePending(client, log); stop {
			return resolved
		}
	}
	balance, err := client.FetchPoints()
	if err != nil {
		log.Message = "查询余额失败: " + err.Error()
		return log
	}
	log.Balance, log.BalanceBefore = balance, balance
	tip := balance - cfg.Collector.Keep
	if tip < cfg.Collector.MinTip {
		log.OK, log.Confirmed = true, true
		log.Message = fmt.Sprintf("余额 %d 不足以归集（保留 %d）", balance, cfg.Collector.Keep)
		return log
	}
	if tip > donateMax {
		tip = donateMax
	}
	topicID, err := e.pickMainTopic(client, cfg)
	if err != nil {
		log.Message = "找主号帖子失败: " + err.Error()
		return log
	}
	log.TopicID, log.TipAmount = topicID, tip
	if cfg.DryRun {
		log.OK, log.Confirmed = true, true
		log.Message = fmt.Sprintf("dry-run：将打赏 %d 积分到 topic/%d", tip, topicID)
		return log
	}
	pendingID, err := e.d.AddTransferPending(&db.TransferRow{
		Time: log.Time, AccountID: log.AccountID, Sub: log.Sub, CheckIn: log.CheckIn,
		Balance: balance, BalanceBefore: balance, TipAmount: tip, TopicID: topicID,
		Pending: true, Retryable: true, Message: "准备提交打赏，等待结果核验",
	})
	if err != nil {
		log.Message = "无法创建待核验记录，已阻止提交打赏: " + err.Error()
		return log
	}
	log.RecordID = pendingID
	for attempt := 1; attempt <= 3; attempt++ {
		log.Attempt = attempt
		result := client.DonateWithBalance(topicID, tip, cfg.Collector.Message, balance)
		log.OK, log.Submitted, log.Confirmed, log.Pending, log.Retryable = result.OK, result.Submitted, result.Confirmed, result.Pending, result.Retryable
		log.HTTP, log.BalanceAfter, log.Message = result.HTTP, result.PointsAfter, result.Message
		if !result.OK && !result.Pending && !result.Retryable {
			log.Confirmed = true
			log.Message = "不可重试条件：" + result.Message
		}
		if result.OK || result.Pending || !result.Retryable || attempt == 3 {
			break
		}
		if !e.waitStop(collectorRetryWait(attempt)) {
			log.Pending = true
			log.Message = "归集停止时结果尚未完成核验"
			break
		}
	}
	if err := e.updateLog(pendingID, log); err != nil {
		e.logf("[归集] %s 更新待核验记录失败: %v", log.Sub, err)
	}
	e.logf("[归集] %s → topic/%d %d 分: confirmed=%v pending=%v %s", log.Sub, topicID, tip, log.Confirmed, log.Pending, log.Message)
	return log
}

func (e *Engine) resolvePending(client *site.Client, base TransferLog) (TransferLog, bool) {
	pending, err := e.d.GetPendingTransfer(base.AccountID, time.Now())
	if err != nil {
		return base, false
	}
	base.RecordID = pending.RecordID
	base.TipAmount, base.TopicID = pending.TipAmount, pending.TopicID
	base.BalanceBefore = pending.BalanceBefore
	balance, balanceErr := client.FetchPoints()
	if balanceErr != nil {
		base.Pending = true
		base.Message = "存在待核验打赏，余额核验失败: " + balanceErr.Error()
		return base, true
	}
	base.Balance, base.BalanceAfter = balance, balance
	pending.BalanceAfter = balance
	switch {
	case pending.BalanceBefore-balance == pending.TipAmount:
		pending.OK, pending.Confirmed, pending.Pending = true, true, false
		pending.Message = fmt.Sprintf("恢复核验确认打赏成功：余额 %d → %d", pending.BalanceBefore, balance)
		_ = e.d.UpdateTransfer(pending.RecordID, pending)
		base.OK, base.Confirmed = true, true
		base.Message = pending.Message
		return base, true
	case balance == pending.BalanceBefore:
		pending.Pending = false
		pending.Message = fmt.Sprintf("待核验请求确认未扣款（%d → %d），允许安全重试", pending.BalanceBefore, balance)
		_ = e.d.UpdateTransfer(pending.RecordID, pending)
		base.RecordID = 0
		return base, false
	default:
		pending.Message = fmt.Sprintf("待核验期间余额发生其他变化（%d → %d），暂不重复提交", pending.BalanceBefore, balance)
		_ = e.d.UpdateTransfer(pending.RecordID, pending)
		base.Pending = true
		base.Message = pending.Message
		return base, true
	}
}

func isRetryableDonate(result site.DonateResult) bool {
	return result.Retryable
}

func (e *Engine) saveLog(log TransferLog) error {
	return e.d.AddTransfer(transferRow(log))
}

func (e *Engine) updateLog(id int64, log TransferLog) error {
	return e.d.UpdateTransfer(id, transferRow(log))
}

func transferRow(log TransferLog) *db.TransferRow {
	return &db.TransferRow{
		Time: log.Time, AccountID: log.AccountID, Sub: log.Sub, CheckIn: log.CheckIn,
		Balance: log.Balance, BalanceBefore: log.BalanceBefore, BalanceAfter: log.BalanceAfter,
		TipAmount: log.TipAmount, TopicID: log.TopicID, DryRun: log.DryRun, OK: log.OK,
		Submitted: log.Submitted, Confirmed: log.Confirmed, Pending: log.Pending,
		Retryable: log.Retryable, HTTP: log.HTTP, Attempt: log.Attempt, Message: log.Message,
	}
}

func (e *Engine) waitStop(duration time.Duration) bool {
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-e.stopCh:
		return false
	case <-timer.C:
		return true
	}
}

func (e *Engine) pickMainTopic(sub *site.Client, cfg config.Config) (int, error) {
	if cfg.Collector.TopicID > 0 {
		return cfg.Collector.TopicID, nil
	}
	mainClient, mainAccount, err := e.mgr.Main()
	if err != nil {
		return 0, fmt.Errorf("主号会话不可用: %w", err)
	}
	if mainAccount.UID == 0 {
		uid, uidErr := mainClient.GetMyUID()
		if uidErr != nil {
			return 0, fmt.Errorf("取主号 UID 失败: %w", uidErr)
		}
		e.mgr.SetUID("main", cfg.Username, uid)
		mainAccount.UID = uid
	}
	status, body, err := sub.RawGet(fmt.Sprintf("/user/%d?tab=topics", mainAccount.UID))
	if err != nil || status != 200 {
		return 0, fmt.Errorf("主号主题页 HTTP %d", status)
	}
	matches := reTopicID.FindAllStringSubmatch(string(body), -1)
	seen := map[string]bool{}
	var topics []int
	for _, match := range matches {
		if !seen[match[1]] {
			seen[match[1]] = true
			id, _ := strconv.Atoi(match[1])
			topics = append(topics, id)
		}
	}
	if len(topics) == 0 {
		return 0, errors.New("主号没有可见主题")
	}
	return topics[rand.Intn(len(topics))], nil
}

// Transfers 返回新到旧的归集记录。
func (e *Engine) Transfers() []TransferLog {
	rows, err := e.d.ListTransfers(500)
	if err != nil {
		return []TransferLog{}
	}
	out := make([]TransferLog, len(rows))
	for i, row := range rows {
		out[i] = TransferLog{
			AccountID: row.AccountID, Time: row.Time, Sub: row.Sub, CheckIn: row.CheckIn,
			Balance: row.Balance, BalanceBefore: row.BalanceBefore, BalanceAfter: row.BalanceAfter,
			TipAmount: row.TipAmount, TopicID: row.TopicID, DryRun: row.DryRun, OK: row.OK,
			Submitted: row.Submitted, Confirmed: row.Confirmed, Pending: row.Pending,
			Retryable: row.Retryable, HTTP: row.HTTP, Attempt: row.Attempt, Message: row.Message,
		}
	}
	return out
}

// Status 给前端展示调度和执行状态。
type Status struct {
	Running         bool   `json:"running"`
	Executing       bool   `json:"executing"`
	NextRun         string `json:"next_run,omitempty"`
	LastRun         string `json:"last_run,omitempty"`
	TopicID         int    `json:"topic_id"`
	Keep            int    `json:"keep"`
	AtHour          int    `json:"at_hour"`
	RandomWindowMin int    `json:"random_window_min"`
}

func (e *Engine) Snapshot() Status {
	e.mu.Lock()
	defer e.mu.Unlock()
	return Status{
		Running: e.running, Executing: e.executing,
		NextRun: formatRun(e.nextRun, e.running), LastRun: formatRun(e.lastRun, !e.lastRun.IsZero()),
		TopicID: e.cfg.Collector.TopicID, Keep: e.cfg.Collector.Keep,
		AtHour: e.cfg.Collector.AtHour, RandomWindowMin: e.cfg.Collector.RandomWindowMin,
	}
}

func (e *Engine) setExecuting(value bool) {
	e.mu.Lock()
	e.executing = value
	e.mu.Unlock()
}

func formatRun(value time.Time, show bool) string {
	if !show || value.IsZero() {
		return ""
	}
	return value.Format("2006-01-02 15:04")
}

func maskUser(username string) string {
	at := strings.IndexByte(username, '@')
	name, domain := username, ""
	if at >= 0 {
		name, domain = username[:at], username[at:]
	}
	runes := []rune(name)
	if len(runes) <= 2 {
		return name + "***" + domain
	}
	return string(runes[:2]) + "***" + domain
}
