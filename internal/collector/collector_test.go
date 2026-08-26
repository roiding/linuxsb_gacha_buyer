package collector

import (
	"path/filepath"
	"sync"
	"testing"
	"time"

	"gacha-buyer/internal/config"
	"gacha-buyer/internal/db"
)

func newTestEngine(t *testing.T, subs []config.SubAccount) (*Engine, *db.DB, func()) {
	t.Helper()
	d, err := db.Open(filepath.Join(t.TempDir(), "collector.db"))
	if err != nil {
		t.Fatal(err)
	}
	cfg := config.Defaults()
	cfg.DryRun = true
	cfg.Subs = subs
	e := New(&cfg, nil, d, t.Logf)
	return e, d, func() { d.Close() }
}

func enabledSubs(names ...string) []config.SubAccount {
	subs := make([]config.SubAccount, 0, len(names))
	for _, n := range names {
		subs = append(subs, config.SubAccount{Username: n, Enabled: true})
	}
	return subs
}

func TestEnsurePlansDistinctFutureMinutes(t *testing.T) {
	e, d, cleanup := newTestEngine(t, enabledSubs("a@x.com", "b@x.com", "c@x.com", "d@x.com", "e@x.com"))
	defer cleanup()
	frozen := time.Date(2026, 8, 25, 10, 0, 0, 0, time.Local)
	e.nowFn = func() time.Time { return frozen }
	day := frozen.Format("2006-01-02")
	e.ensurePlans(day, frozen)

	plans, err := d.GetCollectorSchedules(day)
	if err != nil || len(plans) != 5 {
		t.Fatalf("计划数错误: %d %v", len(plans), err)
	}
	seen := map[int]bool{}
	for _, p := range plans {
		if !p.PlannedAt.After(frozen) || !p.PlannedAt.Before(frozen.Add(24*time.Hour)) {
			t.Fatalf("今天的计划应在剩余时段内: %v", p.PlannedAt)
		}
		lt := p.PlannedAt.In(frozen.Location())
		mod := lt.Hour()*60 + lt.Minute()
		if seen[mod] {
			t.Fatalf("每号时刻应互不重复: %v", p.PlannedAt)
		}
		seen[mod] = true
	}
}

func TestEnsurePlansIdempotent(t *testing.T) {
	e, d, cleanup := newTestEngine(t, enabledSubs("a@x.com", "b@x.com"))
	defer cleanup()
	frozen := time.Date(2026, 8, 25, 10, 0, 0, 0, time.Local)
	e.nowFn = func() time.Time { return frozen }
	day := frozen.Format("2006-01-02")
	e.ensurePlans(day, frozen)
	e.ensurePlans(day, frozen)

	plans, err := d.GetCollectorSchedules(day)
	if err != nil || len(plans) != 2 {
		t.Fatalf("重复生成不应增加计划行: %d %v", len(plans), err)
	}
}

func TestEnsurePlansTomorrowCoversFullDay(t *testing.T) {
	e, d, cleanup := newTestEngine(t, enabledSubs("a@x.com", "b@x.com", "c@x.com"))
	defer cleanup()
	frozen := time.Date(2026, 8, 25, 10, 0, 0, 0, time.Local)
	e.nowFn = func() time.Time { return frozen }
	tomorrow := frozen.AddDate(0, 0, 1)
	day := tomorrow.Format("2006-01-02")
	e.ensurePlans(day, frozen)

	plans, err := d.GetCollectorSchedules(day)
	if err != nil || len(plans) != 3 {
		t.Fatalf("计划数错误: %d %v", len(plans), err)
	}
	start := time.Date(tomorrow.Year(), tomorrow.Month(), tomorrow.Day(), 0, 0, 0, 0, frozen.Location())
	end := start.Add(24 * time.Hour)
	seen := map[int]bool{}
	for _, p := range plans {
		if p.PlannedAt.Before(start) || !p.PlannedAt.Before(end) {
			t.Fatalf("次日计划应落在 0–24 点: %v", p.PlannedAt)
		}
		lt := p.PlannedAt.In(frozen.Location())
		mod := lt.Hour()*60 + lt.Minute()
		if seen[mod] {
			t.Fatalf("每号时刻应互不重复: %v", p.PlannedAt)
		}
		seen[mod] = true
	}
}

func TestRunOnceUpdatesPlans(t *testing.T) {
	orig := collectorAccountWait
	collectorAccountWait = func() time.Duration { return 0 }
	defer func() { collectorAccountWait = orig }()

	e, d, cleanup := newTestEngine(t, enabledSubs("ok@x.com", "pending@x.com", "hard@x.com"))
	defer cleanup()
	frozen := time.Date(2026, 8, 25, 10, 0, 0, 0, time.Local)
	e.nowFn = func() time.Time { return frozen }
	e.collectFn = func(cfg config.Config, sub config.SubAccount) TransferLog {
		log := TransferLog{Time: frozen, Sub: maskUser(sub.Username), AccountID: 1, DryRun: true, Retryable: true}
		switch sub.Username {
		case "pending@x.com":
			log.Pending = true
			log.Message = "待核验"
		case "hard@x.com":
			log.TipAmount = 30
			log.Retryable = false
			log.Confirmed = true
			log.Message = "硬条件未满足"
		default:
			log.OK, log.Confirmed = true, true
			log.TipAmount = 20
		}
		return log
	}

	results := e.RunOnce(false)
	if len(results) != 3 {
		t.Fatalf("应执行 3 个小号: %d", len(results))
	}
	day := frozen.Format("2006-01-02")
	plans, err := d.GetCollectorSchedules(day)
	if err != nil || len(plans) != 3 {
		t.Fatalf("计划读取失败: %+v %v", plans, err)
	}
	byAccount := map[string]*db.CollectorSchedule{}
	for _, p := range plans {
		byAccount[p.Account] = p
	}
	if p := byAccount["ok@x.com"]; p == nil || p.Status != "completed" {
		t.Fatalf("成功小号应为 completed: %+v", p)
	}
	if p := byAccount["pending@x.com"]; p == nil || p.Status != "retry" || p.PlannedAt.Sub(frozen) != retryDelay {
		t.Fatalf("待核验小号应为 retry+15min: %+v", p)
	}
	if p := byAccount["hard@x.com"]; p == nil || p.Status != "completed" {
		t.Fatalf("硬条件小号应为 completed 不重试: %+v", p)
	}
}

func TestRescheduleRegeneratesOnlyPlanned(t *testing.T) {
	e, d, cleanup := newTestEngine(t, enabledSubs("a@x.com", "b@x.com"))
	defer cleanup()
	frozen := time.Date(2026, 8, 25, 10, 0, 0, 0, time.Local)
	e.nowFn = func() time.Time { return frozen }
	day := frozen.Format("2006-01-02")

	// 预置：a 已完成（应保留），b 待执行（应重排）。
	_ = d.SaveCollectorSchedule(&db.CollectorSchedule{Day: day, Account: "a@x.com", PlannedAt: frozen.Add(-time.Hour), CompletedAt: frozen.Add(-30 * time.Minute), Status: "completed"})
	_ = d.SaveCollectorSchedule(&db.CollectorSchedule{Day: day, Account: "b@x.com", PlannedAt: frozen.Add(time.Hour), Status: "planned"})

	e.Reschedule()

	plans, err := d.GetCollectorSchedules(day)
	if err != nil || len(plans) != 2 {
		t.Fatalf("计划数错误: %d %v", len(plans), err)
	}
	for _, p := range plans {
		if p.Account == "a@x.com" && p.Status != "completed" {
			t.Fatalf("已完成小号不应被重排: %+v", p)
		}
		if p.Account == "b@x.com" && (p.Status != "planned" || !p.PlannedAt.After(frozen)) {
			t.Fatalf("待执行小号应重排为未来时刻: %+v", p)
		}
	}
}

func TestScheduledFireRunsAccountOnce(t *testing.T) {
	e, d, cleanup := newTestEngine(t, enabledSubs("fire@x.com"))
	defer cleanup()

	var mu sync.Mutex
	called := 0
	e.collectFn = func(cfg config.Config, sub config.SubAccount) TransferLog {
		mu.Lock()
		called++
		mu.Unlock()
		return TransferLog{Time: time.Now(), Sub: maskUser(sub.Username), AccountID: 1, DryRun: true, OK: true, Confirmed: true}
	}

	day := time.Now().Format("2006-01-02")
	at := time.Now().Add(300 * time.Millisecond)
	if err := d.SaveCollectorSchedule(&db.CollectorSchedule{Day: day, Account: "fire@x.com", PlannedAt: at, Status: "planned"}); err != nil {
		t.Fatal(err)
	}
	e.Start()
	defer e.Stop()

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		mu.Lock()
		n := called
		mu.Unlock()
		if n >= 1 {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	mu.Lock()
	n := called
	mu.Unlock()
	if n != 1 {
		t.Fatalf("一次性任务应恰好触发 1 次，实际 %d", n)
	}
	plans, err := d.GetCollectorSchedules(day)
	if err != nil || len(plans) != 1 || plans[0].Status != "completed" {
		t.Fatalf("计划应标记为完成: %+v %v", plans, err)
	}
}

func TestSnapshotPlansAndNextRun(t *testing.T) {
	e, d, cleanup := newTestEngine(t, enabledSubs("a@x.com", "b@x.com"))
	defer cleanup()
	frozen := time.Date(2026, 8, 25, 10, 0, 0, 0, time.Local)
	e.nowFn = func() time.Time { return frozen }
	day := frozen.Format("2006-01-02")
	// a 已完成、b 未来待执行。
	_ = d.SaveCollectorSchedule(&db.CollectorSchedule{Day: day, Account: "a@x.com", PlannedAt: frozen.Add(-time.Hour), CompletedAt: frozen.Add(-30 * time.Minute), Status: "completed"})
	_ = d.SaveCollectorSchedule(&db.CollectorSchedule{Day: day, Account: "b@x.com", PlannedAt: frozen.Add(2 * time.Hour), Status: "planned"})

	st := e.Snapshot()
	if st.NextRun == "" || st.NextRun != frozen.Add(2*time.Hour).Local().Format("2006-01-02 15:04") {
		t.Fatalf("NextRun 错误: %q", st.NextRun)
	}
	if len(st.Plans) != 2 {
		t.Fatalf("plans 数量错误: %d", len(st.Plans))
	}
	byAccount := map[string]Plan{}
	for _, p := range st.Plans {
		byAccount[p.Account] = p
	}
	if p, ok := byAccount["a***@x.com"]; !ok || p.Status != "completed" {
		t.Fatalf("已完成小号应出现在 plans 中: %+v", p)
	}
	if p, ok := byAccount["b***@x.com"]; !ok || p.Status != "planned" || p.PlannedAt == "" {
		t.Fatalf("待执行小号应出现在 plans 中: %+v", p)
	}
}

func TestRetryNeeded(t *testing.T) {
	cases := []struct {
		name string
		log  TransferLog
		want bool
	}{
		{"打赏待核验", TransferLog{Pending: true}, true},
		{"可重试失败且打赏>0", TransferLog{TipAmount: 20, Retryable: true}, true},
		{"硬条件失败不重试", TransferLog{TipAmount: 20, Retryable: false}, false},
		{"签到等打赏前失败也重试", TransferLog{Retryable: true}, true},
		{"确认过的失败不重试", TransferLog{Retryable: false, Confirmed: true}, false},
		{"抽卡未落定", TransferLog{OK: true, GachaPending: true}, true},
		{"全部完成", TransferLog{OK: true, Confirmed: true, TipAmount: 20}, false},
		{"空包无打赏", TransferLog{OK: true}, false},
	}
	for _, c := range cases {
		if got := retryNeeded(c.log); got != c.want {
			t.Fatalf("%s: retryNeeded=%v want %v", c.name, got, c.want)
		}
	}
}

func TestGachaPendingDrivesPlanRetry(t *testing.T) {
	orig := collectorAccountWait
	collectorAccountWait = func() time.Duration { return 0 }
	defer func() { collectorAccountWait = orig }()

	e, d, cleanup := newTestEngine(t, enabledSubs("gacha@x.com"))
	defer cleanup()
	frozen := time.Date(2026, 8, 25, 10, 0, 0, 0, time.Local)
	e.nowFn = func() time.Time { return frozen }
	// 打赏已完成（今日已处理）但抽卡赠送未落定 → 整轮应 retry。
	e.collectFn = func(cfg config.Config, sub config.SubAccount) TransferLog {
		return TransferLog{
			Time: frozen, Sub: maskUser(sub.Username), AccountID: 1, DryRun: true,
			OK: true, Confirmed: true, GachaPending: true, Message: "今日已处理，跳过",
		}
	}
	results := e.RunOnce(false)
	if len(results) != 1 || !results[0].GachaPending {
		t.Fatalf("应返回 GachaPending 结果: %+v", results)
	}
	day := frozen.Format("2006-01-02")
	plans, err := d.GetCollectorSchedules(day)
	if err != nil || len(plans) != 1 {
		t.Fatalf("计划读取失败: %+v %v", plans, err)
	}
	if p := plans[0]; p.Status != "retry" || p.PlannedAt.Sub(frozen) != retryDelay {
		t.Fatalf("抽卡未落定应 retry+15min: %+v", p)
	}
}

func TestSyncPlansAddsNewSubSameDay(t *testing.T) {
	e, d, cleanup := newTestEngine(t, enabledSubs("a@x.com"))
	defer cleanup()
	frozen := time.Date(2026, 8, 26, 12, 0, 0, 0, time.Local)
	e.nowFn = func() time.Time { return frozen }
	day := frozen.Format("2006-01-02")
	e.ensurePlans(day, frozen)
	plans0, err := d.GetCollectorSchedules(day)
	if err != nil || len(plans0) != 1 {
		t.Fatalf("初始计划生成失败: %+v %v", plans0, err)
	}
	aBefore := plans0[0].PlannedAt

	// 运行中途通过 UI 新增小号：补排计划不应影响已排定的 a。
	e.cfg.Subs = append(e.cfg.Subs, config.SubAccount{Username: "b@x.com", Enabled: true})
	e.SyncPlans()

	plans, err := d.GetCollectorSchedules(day)
	if err != nil || len(plans) != 2 {
		t.Fatalf("新号应立即有当天计划: %+v %v", plans, err)
	}
	byAccount := map[string]*db.CollectorSchedule{}
	for _, p := range plans {
		byAccount[p.Account] = p
	}
	a, b := byAccount["a@x.com"], byAccount["b@x.com"]
	if a == nil || !a.PlannedAt.Equal(aBefore) {
		t.Fatalf("原有计划不应被重排: %+v (之前 %v)", a, aBefore)
	}
	if b == nil || b.Status != "planned" || !b.PlannedAt.After(frozen) {
		t.Fatalf("新增小号应补排为当天未来时刻: %+v", b)
	}
}

func TestPreTipFailureRetries(t *testing.T) {
	e, d, cleanup := newTestEngine(t, enabledSubs("fail@x.com"))
	defer cleanup()
	frozen := time.Date(2026, 8, 25, 10, 0, 0, 0, time.Local)
	e.nowFn = func() time.Time { return frozen }
	// 模拟签到失败：发生在打赏之前，TipAmount 尚未赋值。
	e.collectFn = func(cfg config.Config, sub config.SubAccount) TransferLog {
		return TransferLog{Time: frozen, Sub: maskUser(sub.Username), Retryable: true, Message: "签到失败"}
	}
	e.RunOnce(false)

	day := frozen.Format("2006-01-02")
	plans, err := d.GetCollectorSchedules(day)
	if err != nil || len(plans) != 1 {
		t.Fatalf("计划读取失败: %+v %v", plans, err)
	}
	if p := plans[0]; p.Status != "retry" || p.PlannedAt.Sub(frozen) != retryDelay {
		t.Fatalf("打赏前失败也应 retry+15min: %+v", p)
	}
}

func TestRetryCapMarksCompleted(t *testing.T) {
	e, d, cleanup := newTestEngine(t, enabledSubs("stuck@x.com"))
	defer cleanup()
	frozen := time.Date(2026, 8, 25, 10, 0, 0, 0, time.Local)
	e.nowFn = func() time.Time { return frozen }
	day := frozen.Format("2006-01-02")
	_ = d.SaveCollectorSchedule(&db.CollectorSchedule{Day: day, Account: "stuck@x.com", PlannedAt: frozen, Status: "retry", Retries: maxDailyRetries})
	e.collectFn = func(cfg config.Config, sub config.SubAccount) TransferLog {
		return TransferLog{Time: frozen, Sub: maskUser(sub.Username), Retryable: true, Message: "持续失败"}
	}
	e.RunOnce(false)

	plans, err := d.GetCollectorSchedules(day)
	if err != nil || len(plans) != 1 {
		t.Fatalf("计划读取失败: %+v %v", plans, err)
	}
	if p := plans[0]; p.Status != "completed" || p.Retries != maxDailyRetries {
		t.Fatalf("超过重试上限应标 completed 不再自增: %+v", p)
	}
}
