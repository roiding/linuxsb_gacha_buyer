// Package collector 每日归集：每个已启用小号在当天 0–24 点拥有独立随机执行时刻，
// 到点后签到并把积分通过打赏主号帖子转给主号。
package collector

import (
	"errors"
	"fmt"
	"math/rand"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/robfig/cron/v3"

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
	// retryDelay 临时失败（可重试）后重新执行的等待时长。
	retryDelay = 15 * time.Minute
	// maxDailyRetries 单号单日最大重试轮数，防止持续失败时每 15 分钟刷到午夜。
	maxDailyRetries = 6
)

// Engine 管理每个小号当天的独立归集时刻，并用 robfig/cron 触发执行。
type Engine struct {
	cfg  *config.Config
	mgr  *accounts.Manager
	d    *db.DB
	logf func(string, ...any)

	mu        sync.Mutex
	running   bool
	executing bool
	lastRun   time.Time
	stopCh    chan struct{}
	runMu     sync.Mutex

	cron *cron.Cron
	jobs map[string]cron.EntryID

	// 测试注入：时钟与归集执行器。
	nowFn     func() time.Time
	collectFn func(cfg config.Config, sub config.SubAccount) TransferLog
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

	GachaTitle   string `json:"-"` // 当日免费一抽所得称号名（仅展示用）
	GachaPending bool   `json:"-"` // 免费抽卡/赠送今日未落定，需要整轮重试
}

// New 创建归集引擎。
func New(cfg *config.Config, mgr *accounts.Manager, d *db.DB, logf func(string, ...any)) *Engine {
	if logf == nil {
		logf = func(string, ...any) {}
	}
	return &Engine{
		cfg: cfg, mgr: mgr, d: d, logf: logf,
		stopCh: make(chan struct{}),
		jobs:   map[string]cron.EntryID{},
	}
}

// now 便于测试注入时钟。
func (e *Engine) now() time.Time {
	if e.nowFn != nil {
		return e.nowFn()
	}
	return time.Now()
}

// doCollect 便于测试替换真实归集执行。
func (e *Engine) doCollect(cfg config.Config, sub config.SubAccount) TransferLog {
	if e.collectFn != nil {
		return e.collectFn(cfg, sub)
	}
	return e.collectOne(cfg, sub)
}

// Start 启动每日调度：补执行已到点的计划、注册未到点的一次性任务，并挂上每日 00:05 翻日任务。
func (e *Engine) Start() {
	e.mu.Lock()
	if e.running {
		e.mu.Unlock()
		return
	}
	e.running = true
	e.stopCh = make(chan struct{})
	e.jobs = map[string]cron.EntryID{}
	c := cron.New()
	e.cron = c
	_, _ = c.AddFunc("5 0 * * *", func() {
		now := e.now()
		e.ensurePlans(now.Format("2006-01-02"), now)
		e.rebuildJobs(now)
	})
	e.mu.Unlock()

	e.recoverStaleRuns(e.now())
	e.rebuildJobs(e.now())
	c.Start()
	e.logf("归集引擎已启动")
}

// Stop 停止调度并等待进行中的可中断等待退出。
func (e *Engine) Stop() {
	e.mu.Lock()
	if !e.running {
		e.mu.Unlock()
		return
	}
	e.running = false
	c := e.cron
	e.mu.Unlock()
	close(e.stopCh)
	if c != nil {
		ctx := c.Stop()
		select {
		case <-ctx.Done():
		case <-time.After(5 * time.Second):
		}
	}
}

// Reschedule 删除当天尚未执行的计划并重新随机化，配置变更后立即生效。
func (e *Engine) Reschedule() {
	now := e.now()
	day := now.Format("2006-01-02")
	if err := e.d.DeleteCollectorSchedules(day); err != nil {
		e.logf("重算归集计划失败: %v", err)
	}
	e.rebuildJobs(now)
}

// SyncPlans 为新增/启用的小号补建当天计划，不影响其它小号已排定的时刻。
func (e *Engine) SyncPlans() {
	e.rebuildJobs(e.now())
}

// rebuildJobs 读当天计划：到点或已过的账号补执行，未到点的注册一次性任务。
func (e *Engine) rebuildJobs(now time.Time) {
	e.mu.Lock()
	defer e.mu.Unlock()
	day := now.Format("2006-01-02")
	e.ensurePlans(day, now)
	plans, err := e.d.GetCollectorSchedules(day)
	if err != nil {
		e.logf("读取归集计划失败: %v", err)
		return
	}
	e.clearJobsLocked()
	if e.cron == nil {
		return
	}
	var catchUp []*db.CollectorSchedule
	for _, p := range plans {
		if p.Status != "planned" && p.Status != "retry" {
			continue
		}
		if !p.PlannedAt.After(now) {
			catchUp = append(catchUp, p)
			continue
		}
		account := p.Account
		id := e.cron.Schedule(oneShotSchedule{at: p.PlannedAt}, cron.FuncJob(func() {
			e.runScheduledAccount(account)
		}))
		e.jobs[account] = id
	}
	if len(catchUp) > 0 {
		go e.runCatchUp(catchUp)
	}
}

// recoverStaleRuns 进程重启时，把上次崩溃遗留的 running 计划恢复为可重试。
func (e *Engine) recoverStaleRuns(now time.Time) {
	plans, err := e.d.GetCollectorSchedules(now.Format("2006-01-02"))
	if err != nil {
		e.logf("读取归集计划失败: %v", err)
		return
	}
	for _, p := range plans {
		if p.Status != "running" {
			continue
		}
		if p.StartedAt.IsZero() || now.Sub(p.StartedAt) <= 10*time.Minute {
			continue
		}
		p.Status = "retry"
		p.PlannedAt = now
		_ = e.d.SaveCollectorSchedule(p)
	}
}

// ensurePlans 确保 day 当天每个已启用小号都有计划行；缺失的补一个随机时刻。
func (e *Engine) ensurePlans(day string, now time.Time) {
	existing, err := e.d.GetCollectorSchedules(day)
	if err != nil {
		e.logf("读取归集计划失败: %v", err)
		return
	}
	has := make(map[string]bool, len(existing))
	for _, p := range existing {
		has[p.Account] = true
	}
	var missing []config.SubAccount
	for _, sub := range e.cfg.Subs {
		if !sub.Enabled || has[sub.Username] {
			continue
		}
		missing = append(missing, sub)
	}
	if len(missing) == 0 {
		return
	}
	dayStart, err := time.ParseInLocation("2006-01-02", day, now.Location())
	if err != nil {
		e.logf("解析计划日期失败(%s): %v", day, err)
		return
	}
	at := e.randomMinutes(len(missing), day == now.Format("2006-01-02"), dayStart, now)
	for i, sub := range missing {
		if i >= len(at) {
			break // 当天剩余分钟不足，其余小号次日再排
		}
		if err := e.d.SaveCollectorSchedule(&db.CollectorSchedule{
			Day: day, Account: sub.Username, PlannedAt: at[i], Status: "planned",
		}); err != nil {
			e.logf("保存归集计划失败(%s): %v", sub.Username, err)
		}
	}
}

// randomMinutes 返回 count 个互不重复的当日随机分钟；生成当天的计划时只取 now 之后的时刻，
// 避免首次部署或中午启动时瞬间全量补跑。
func (e *Engine) randomMinutes(count int, today bool, dayStart, now time.Time) []time.Time {
	out := make([]time.Time, 0, count)
	for _, minute := range rand.Perm(1440) {
		if len(out) == count {
			break
		}
		at := dayStart.Add(time.Duration(minute) * time.Minute)
		if today && !at.After(now) {
			continue
		}
		out = append(out, at)
	}
	return out
}

// oneShotSchedule 到点触发一次的一次性 cron 计划；触发后 Next 返回零值，由框架自动移除。
type oneShotSchedule struct {
	at time.Time
}

func (s oneShotSchedule) Next(t time.Time) time.Time {
	if s.at.After(t) {
		return s.at
	}
	return time.Time{}
}

// runScheduledAccount 由一次性 cron 任务触发的单号归集。
func (e *Engine) runScheduledAccount(account string) {
	if !e.isRunning() {
		return
	}
	cfg := *e.cfg
	cfg.Subs = append([]config.SubAccount(nil), e.cfg.Subs...)
	sub, ok := e.findSubByUsername(cfg.Subs, account)
	if !ok || !sub.Enabled {
		return
	}
	e.setExecuting(true)
	defer e.setExecuting(false)
	e.runMu.Lock()
	defer e.runMu.Unlock()
	if !e.isRunning() {
		return
	}
	// 等待 runMu 期间可能已被手动/补执行轮处理完毕，不再重复执行。
	if p := e.getPlan(e.now().Format("2006-01-02"), account); p != nil && p.Status == "completed" {
		return
	}
	e.collectAndSchedule(cfg, sub)
}

// runCatchUp 批量补执行到点但尚未运行的小号（启动恢复、定时触发与手动执行之外的重叠场景）。
func (e *Engine) runCatchUp(plans []*db.CollectorSchedule) {
	e.setExecuting(true)
	defer e.setExecuting(false)
	e.runMu.Lock()
	defer e.runMu.Unlock()
	if !e.isRunning() {
		return
	}
	cfg := *e.cfg
	cfg.Subs = append([]config.SubAccount(nil), e.cfg.Subs...)
	for i, p := range plans {
		select {
		case <-e.stopCh:
			return
		default:
		}
		sub, ok := e.findSubByUsername(cfg.Subs, p.Account)
		if !ok || !sub.Enabled {
			continue
		}
		e.collectAndSchedule(cfg, sub)
		if i < len(plans)-1 && !e.waitStop(collectorAccountWait()) {
			return
		}
	}
}

// collectAndSchedule 执行单个小号归集并更新其当天计划行。
func (e *Engine) collectAndSchedule(cfg config.Config, sub config.SubAccount) TransferLog {
	now := e.now()
	day := now.Format("2006-01-02")
	plan := e.getPlan(day, sub.Username)
	if plan != nil {
		plan.Status = "running"
		if plan.StartedAt.IsZero() {
			plan.StartedAt = now
		}
		if err := e.d.SaveCollectorSchedule(plan); err != nil {
			e.logf("[归集] %s 更新运行中状态失败: %v", sub.Username, err)
		}
	}
	log := e.doCollect(cfg, sub)
	if log.RecordID == 0 {
		if err := e.saveLog(log); err != nil {
			e.logf("[归集] %s 记录保存失败: %v", log.Sub, err)
		}
	}
	if plan == nil {
		return log
	}
	now = e.now()
	retry := retryNeeded(log)
	if retry && plan.Retries >= maxDailyRetries {
		e.logf("[归集] %s 今日已重试 %d 次仍未落定，放弃至明天", sub.Username, plan.Retries)
		retry = false
	}
	if retry {
		plan.Status = "retry"
		plan.Retries++
		plan.PlannedAt = now.Add(retryDelay)
		plan.CompletedAt = time.Time{}
	} else {
		plan.Status = "completed"
		plan.CompletedAt = now
	}
	if err := e.d.SaveCollectorSchedule(plan); err != nil {
		e.logf("[归集] %s 更新计划状态失败: %v", sub.Username, err)
	}
	switch plan.Status {
	case "completed":
		e.removeJob(sub.Username)
	case "retry":
		e.registerJob(sub.Username, plan.PlannedAt)
	}
	e.mu.Lock()
	e.lastRun = now
	e.mu.Unlock()
	return log
}

func (e *Engine) getPlan(day, account string) *db.CollectorSchedule {
	plans, err := e.d.GetCollectorSchedules(day)
	if err != nil {
		e.logf("读取归集计划失败: %v", err)
		return nil
	}
	for _, p := range plans {
		if p.Account == account {
			return p
		}
	}
	return nil
}

func (e *Engine) findSubByUsername(subs []config.SubAccount, username string) (config.SubAccount, bool) {
	for _, s := range subs {
		if s.Username == username {
			return s, true
		}
	}
	return config.SubAccount{}, false
}

func (e *Engine) registerJob(account string, at time.Time) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.cron == nil {
		return
	}
	e.removeJobLocked(account)
	id := e.cron.Schedule(oneShotSchedule{at: at}, cron.FuncJob(func() {
		e.runScheduledAccount(account)
	}))
	e.jobs[account] = id
}

func (e *Engine) removeJob(account string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.removeJobLocked(account)
}

func (e *Engine) removeJobLocked(account string) {
	if e.cron == nil {
		return
	}
	if id, ok := e.jobs[account]; ok {
		e.cron.Remove(id)
		delete(e.jobs, account)
	}
}

func (e *Engine) clearJobsLocked() {
	if e.cron == nil {
		e.jobs = map[string]cron.EntryID{}
		return
	}
	for account, id := range e.jobs {
		e.cron.Remove(id)
		delete(e.jobs, account)
	}
}

func (e *Engine) isRunning() bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.running
}

func (e *Engine) setExecuting(value bool) {
	e.mu.Lock()
	e.executing = value
	e.mu.Unlock()
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

func (e *Engine) runOnceLocked(_ bool) []TransferLog {
	cfg := *e.cfg
	cfg.Subs = append([]config.SubAccount(nil), e.cfg.Subs...)
	now := e.now()
	e.ensurePlans(now.Format("2006-01-02"), now)
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
		log := e.collectAndSchedule(cfg, sub)
		results = append(results, log)
		if i < len(enabled)-1 && !e.waitStop(collectorAccountWait()) {
			interrupted = true
			break
		}
	}
	return results
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
	if !cfg.DryRun {
		// 每日免费一抽 + 赠送主号：即使打赏今日已处理也照做；未落定则整轮重试。
		title, pending := e.doGachaTask(client, account.ID, sub, cfg)
		log.GachaTitle, log.GachaPending = title, pending
	}
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
		log.Message = fmt.Sprintf("dry-run：将每日免费一抽并赠送主号；打赏 %d 积分到 topic/%d", tip, topicID)
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

// retryNeeded 该轮结果是否还需要今日再跑：打赏待核验/任何可重试失败（含打赏前的
// 取会话、签到、查余额、找帖子），或免费抽卡/赠送未落定。
func retryNeeded(log TransferLog) bool {
	return log.Pending || (!log.OK && !log.Confirmed && log.Retryable) || log.GachaPending
}

// doGachaTask 完成小号每日免费一抽并尝试把当日抽到的称号赠送给主号。
// 返回 (drawnTitle, pending)：pending 表示抽卡或赠送今日尚未落定，需要整轮重试。
func (e *Engine) doGachaTask(client *site.Client, accountID int, sub config.SubAccount, cfg config.Config) (string, bool) {
	now := e.now()
	day := now.Format("2006-01-02")
	rec, _ := e.d.GetGachaDraw(day, accountID)
	if rec != nil && rec.OK {
		// 今日已抽。若有所得且尚未赠送成功，重试轮里补一次赠送。
		if rec.Drawn != "" && !rec.Gifted {
			g := e.giftDrawnTitle(client, rec.Drawn, cfg)
			rec.Gifted, rec.GiftTarget = g.Gifted, g.Target
			rec.Message = joinGachaMsg(rec.Message, "赠送: "+g.Message)
			if err := e.d.SaveGachaDraw(rec); err != nil {
				e.logf("[归集] %s 更新抽卡赠送状态失败: %v", sub.Username, err)
			}
			return rec.Drawn, !g.Gifted
		}
		return rec.Drawn, false
	}
	// 今日首次抽，或上次抽卡请求失败（OK=false）重新抽。
	res := client.DrawGacha()
	row := &db.GachaDrawRow{
		Day: day, Time: now, AccountID: accountID, Sub: maskUser(sub.Username),
		OK: res.OK, Drawn: res.Title, Message: res.Message,
	}
	if res.Drawn && res.Title != "" {
		// 只有抽到称号才有所得可赠送；空包/请求失败不赠送。
		g := e.giftDrawnTitle(client, res.Title, cfg)
		row.Gifted, row.GiftTarget = g.Gifted, g.Target
		row.Message = joinGachaMsg(row.Message, "赠送: "+g.Message)
	}
	if err := e.d.SaveGachaDraw(row); err != nil {
		e.logf("[归集] %s 保存抽卡记录失败: %v", sub.Username, err)
	}
	if !res.OK {
		return res.Title, true // 抽卡请求失败 → 重试
	}
	if res.Drawn && res.Title != "" && !row.Gifted {
		return res.Title, true // 抽到但未赠送成功 → 重试
	}
	return res.Title, false
}

// giftDrawnTitle 把抽到的称号赠送给主号并记录日志。
func (e *Engine) giftDrawnTitle(client *site.Client, title string, cfg config.Config) site.GiftResult {
	target := e.mainGiftTarget(client)
	g := client.GiftTitle(target, title)
	e.logf("[归集] 赠送「%s」→ %s: gifted=%v %s", title, target, g.Gifted, g.Message)
	return g
}

// mainGiftTarget 返回主号在站点的显示用户名（赠送接收方）。
// 优先用主号 UID 反查用户页 title；查不到时退回配置中的主号用户名。
// 返回前统一清理首尾空白（含不换行空格），避免赠送目标不匹配。
func (e *Engine) mainGiftTarget(sub *site.Client) string {
	target := e.cfg.Username
	if acct, err := e.d.GetAccount("main", e.cfg.Username); err == nil && acct != nil && acct.UID > 0 {
		if status, body, gErr := sub.RawGet(fmt.Sprintf("/user/%d", acct.UID)); gErr == nil && status == 200 {
			if name := site.UsernameFromProfilePage(string(body)); name != "" {
				return name
			}
		}
	}
	return site.CleanUsername(target)
}

func joinGachaMsg(prev, add string) string {
	if add == "" {
		return prev
	}
	if prev == "" {
		return add
	}
	return prev + "；" + add
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

// Transfers 返回新到旧的归集记录（最多 500 条；分页请用 TransfersPage）。
func (e *Engine) Transfers() []TransferLog {
	return e.TransfersPage(0, 500)
}

// TransfersPage 分页返回归集记录（新→旧，offset 从 0 开始）。
func (e *Engine) TransfersPage(offset, limit int) []TransferLog {
	rows, err := e.d.ListTransfersPage(offset, limit)
	if err != nil {
		return []TransferLog{}
	}
	return transferLogs(rows)
}

// TransfersCount 归集记录总数。
func (e *Engine) TransfersCount() int { return e.d.CountTransfers() }

// transferLogs 把 db 行映射为前端展示模型。
func transferLogs(rows []*db.TransferRow) []TransferLog {
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
	Running   bool   `json:"running"`
	Executing bool   `json:"executing"`
	NextRun   string `json:"next_run,omitempty"`
	LastRun   string `json:"last_run,omitempty"`
	TopicID   int    `json:"topic_id"`
	Keep      int    `json:"keep"`
	Plans     []Plan `json:"plans,omitempty"`
}

// Plan 某个小号当天的计划行（给前端看分散情况）。
type Plan struct {
	Account     string `json:"account"`
	PlannedAt   string `json:"planned_at"`
	StartedAt   string `json:"started_at,omitempty"`
	CompletedAt string `json:"completed_at,omitempty"`
	Status      string `json:"status"`
}

func (e *Engine) Snapshot() Status {
	now := e.now()
	plans, err := e.d.GetCollectorSchedules(now.Format("2006-01-02"))
	if err != nil {
		e.logf("读取归集计划失败: %v", err)
		plans = nil
	}
	st := Status{
		TopicID: e.cfg.Collector.TopicID,
		Keep:    e.cfg.Collector.Keep,
	}
	e.mu.Lock()
	st.Running, st.Executing = e.running, e.executing
	st.LastRun = formatRun(e.lastRun, !e.lastRun.IsZero())
	e.mu.Unlock()
	st.NextRun = formatRun(e.nextRunTime(plans, now), true)
	for _, p := range plans {
		st.Plans = append(st.Plans, Plan{
			Account:     maskUser(p.Account),
			PlannedAt:   formatRun(p.PlannedAt, !p.PlannedAt.IsZero()),
			StartedAt:   formatRun(p.StartedAt, !p.StartedAt.IsZero()),
			CompletedAt: formatRun(p.CompletedAt, !p.CompletedAt.IsZero()),
			Status:      p.Status,
		})
	}
	sort.Slice(st.Plans, func(i, j int) bool { return st.Plans[i].PlannedAt < st.Plans[j].PlannedAt })
	return st
}

// nextRunTime 当天最早未完成（planned/retry）的计划时刻；已有到点未跑的计划时返回 now。
func (e *Engine) nextRunTime(plans []*db.CollectorSchedule, now time.Time) time.Time {
	var earliest time.Time
	for _, p := range plans {
		if p.Status != "planned" && p.Status != "retry" {
			continue
		}
		if !p.PlannedAt.After(now) {
			if earliest.IsZero() || now.Before(earliest) {
				earliest = now
			}
			continue
		}
		if earliest.IsZero() || p.PlannedAt.Before(earliest) {
			earliest = p.PlannedAt
		}
	}
	return earliest
}

func formatRun(value time.Time, show bool) string {
	if !show || value.IsZero() {
		return ""
	}
	return value.Local().Format("2006-01-02 15:04")
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
