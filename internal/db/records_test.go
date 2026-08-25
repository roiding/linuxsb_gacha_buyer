package db

import (
	"database/sql"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"
)

func TestPurchaseJSONAndConfirmedStats(t *testing.T) {
	d, err := Open(filepath.Join(t.TempDir(), "records.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()

	p := &PurchaseRow{
		Time: time.Date(2026, 8, 23, 12, 0, 0, 0, time.UTC), ListingID: 7,
		Name: "欧皇", Rarity: "ssr", Price: 20, Qty: 2, Cost: 40,
		Submitted: true, Confirmed: true, OK: true, Message: "成交已确认",
	}
	if err := d.AddPurchase(p); err != nil {
		t.Fatal(err)
	}
	if err := d.AddPurchase(&PurchaseRow{Time: time.Now(), ListingID: 8, Name: "失败", Price: 20, Qty: 2, Cost: 40, Submitted: true, Message: "未确认成交"}); err != nil {
		t.Fatal(err)
	}
	rows, err := d.ListPurchases(10)
	if err != nil || len(rows) != 2 {
		t.Fatalf("ListPurchases: %v, %d rows", err, len(rows))
	}
	var raw map[string]any
	data, err := json.Marshal(rows[1])
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatal(err)
	}
	if _, ok := raw["time"]; !ok {
		t.Fatalf("购买记录 JSON 缺少 time: %s", data)
	}
	if _, ok := raw["confirmed"]; !ok {
		t.Fatalf("购买记录 JSON 缺少 confirmed: %s", data)
	}
	stats, err := d.GetPurchaseStats()
	if err != nil {
		t.Fatal(err)
	}
	if stats.OKCount != 1 || stats.TotalSpent != 40 {
		t.Fatalf("统计错误: %+v", stats)
	}
}

func TestLotteryReplyDedupAndCooldown(t *testing.T) {
	d, err := Open(filepath.Join(t.TempDir(), "lottery.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()

	now := time.Now()
	pending := &LotteryReplyRow{
		Time: now, AccountID: 9, Sub: "su***", TopicID: 15458,
		Content: "参与抽奖好运", Submitted: true, Message: "未确认",
	}
	if err := d.AddLotteryReply(pending); err != nil {
		t.Fatal(err)
	}
	if !d.LotteryReplyPendingRecently(9, 15458, now.Add(-time.Hour)) || d.LotteryReplyConfirmed(9, 15458) {
		t.Fatal("未确认提交的冷却状态错误")
	}

	confirmed := &LotteryReplyRow{
		Time: now.Add(time.Minute), AccountID: 9, Sub: "su***", TopicID: 15458,
		Content: "再次参与抽奖", Submitted: true, Confirmed: true, ReplyID: 777, Message: "已确认",
	}
	if err := d.AddLotteryReply(confirmed); err != nil {
		t.Fatal(err)
	}
	if !d.LotteryReplyConfirmed(9, 15458) {
		t.Fatal("确认回复应触发永久去重")
	}

	rows, err := d.ListLotteryReplies(10)
	if err != nil || len(rows) != 2 || rows[0].ReplyID != 777 || !rows[0].Confirmed {
		t.Fatalf("回复记录查询错误: rows=%+v err=%v", rows, err)
	}
}

func TestTransferConfirmationPendingAndSchedule(t *testing.T) {
	d, err := Open(filepath.Join(t.TempDir(), "transfer.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()

	now := time.Now()
	if err := d.AddTransfer(&TransferRow{Time: now, AccountID: 7, Sub: "小号***", TipAmount: 20, DryRun: true, OK: true, Confirmed: true}); err != nil {
		t.Fatal(err)
	}
	if d.TippedToday(7, now) || d.TransferHandledToday(7, now) {
		t.Fatal("dry-run 不应阻止真实当日归集")
	}
	id, err := d.AddTransferPending(&TransferRow{Time: now, AccountID: 7, Sub: "小号***", Balance: 100, BalanceBefore: 100, TipAmount: 20, Pending: true, Attempt: 1})
	if err != nil || id <= 0 {
		t.Fatalf("预写 pending 失败: id=%d err=%v", id, err)
	}
	pending, err := d.GetPendingTransfer(7, now)
	if err != nil || pending.RecordID != id || !pending.Pending {
		t.Fatalf("pending 查询错误: %+v %v", pending, err)
	}
	pending.BalanceAfter = 80
	pending.Pending, pending.Confirmed, pending.OK = false, true, true
	if err := d.UpdateTransfer(id, pending); err != nil {
		t.Fatal(err)
	}
	if !d.TippedToday(7, now) || !d.TransferHandledToday(7, now) {
		t.Fatal("确认记录应触发当日去重")
	}

	schedule := &CollectorSchedule{Day: "2026-08-23", Account: "sub1", PlannedAt: now, Status: "planned"}
	if err := d.SaveCollectorSchedule(schedule); err != nil {
		t.Fatal(err)
	}
	if err := d.SaveCollectorSchedule(&CollectorSchedule{Day: "2026-08-23", Account: "sub2", PlannedAt: now.Add(time.Hour), Status: "planned"}); err != nil {
		t.Fatal(err)
	}
	if err := d.SaveCollectorSchedule(&CollectorSchedule{Day: "2026-08-24", Account: "sub1", PlannedAt: now.Add(24 * time.Hour), Status: "planned"}); err != nil {
		t.Fatal(err)
	}
	plans, err := d.GetCollectorSchedules("2026-08-23")
	if err != nil || len(plans) != 2 {
		t.Fatalf("计划读取错误: %+v %v", plans, err)
	}
	if plans[0].Account == "" || plans[0].Status != "planned" || plans[0].PlannedAt.IsZero() {
		t.Fatalf("计划字段错误: %+v", plans[0])
	}
	// 按 day+account 去重：再次保存同一账号应覆盖而不是新增。
	schedule.Status = "completed"
	if err := d.SaveCollectorSchedule(schedule); err != nil {
		t.Fatal(err)
	}
	plans, err = d.GetCollectorSchedules("2026-08-23")
	if err != nil || len(plans) != 2 {
		t.Fatalf("去重保存后计划数错误: %d %v", len(plans), err)
	}
	if err := d.DeleteCollectorSchedules("2026-08-23"); err != nil {
		t.Fatal(err)
	}
	plans, err = d.GetCollectorSchedules("2026-08-23")
	if err != nil || len(plans) != 1 || plans[0].Account != "sub1" || plans[0].Status != "completed" {
		t.Fatalf("删除应只清掉 planned 行，保留 completed 的 sub1: %+v %v", plans, err)
	}
	// 其他天的计划不受影响。
	plans, err = d.GetCollectorSchedules("2026-08-24")
	if err != nil || len(plans) != 1 {
		t.Fatalf("跨天删除误伤其他计划: %+v %v", plans, err)
	}
}

func TestCollectorScheduleMigrationRebuildsOldTable(t *testing.T) {
	path := filepath.Join(t.TempDir(), "migrate.db")
	raw, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	// 模拟旧版单计划点表结构（day 为主键、无 account 列）。
	if _, err := raw.Exec(`CREATE TABLE collector_schedule (
		day TEXT PRIMARY KEY, planned_at TEXT NOT NULL, started_at TEXT NOT NULL DEFAULT '',
		completed_at TEXT NOT NULL DEFAULT '', status TEXT NOT NULL DEFAULT 'planned')`); err != nil {
		t.Fatal(err)
	}
	if _, err := raw.Exec(`INSERT INTO collector_schedule(day, planned_at, status) VALUES('2026-08-24','2026-08-24T09:00:00Z','planned')`); err != nil {
		t.Fatal(err)
	}
	raw.Close()

	d, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	ok, err := hasColumn(d.DB, "collector_schedule", "account")
	if err != nil || !ok {
		t.Fatalf("迁移后应具备 account 列: ok=%v err=%v", ok, err)
	}
	// 旧表数据属于临时计划，重建后应被清空。
	rows, err := d.GetCollectorSchedules("2026-08-24")
	if err != nil || len(rows) != 0 {
		t.Fatalf("旧计划数据应被清空: %+v %v", rows, err)
	}
}

func TestGachaDrawsSaveGetUpsertList(t *testing.T) {
	d, err := Open(filepath.Join(t.TempDir(), "gacha.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	now := time.Date(2026, 8, 25, 10, 0, 0, 0, time.UTC)

	if err := d.SaveGachaDraw(&GachaDrawRow{
		Day: "2026-08-25", Time: now, AccountID: 1, Sub: "a***@x.com",
		Drawn: "键盘侠", OK: true, Gifted: true, GiftTarget: "Se7en", Message: "获得称号「键盘侠」",
	}); err != nil {
		t.Fatal(err)
	}
	rec, err := d.GetGachaDraw("2026-08-25", 1)
	if err != nil || rec == nil {
		t.Fatalf("读取抽卡记录失败: %v %v", rec, err)
	}
	if rec.Drawn != "键盘侠" || !rec.OK || !rec.Gifted || rec.GiftTarget != "Se7en" {
		t.Fatalf("记录字段错误: %+v", rec)
	}

	// 同日同账号 upsert 覆盖旧记录。
	if err := d.SaveGachaDraw(&GachaDrawRow{
		Day: "2026-08-25", Time: now.Add(time.Hour), AccountID: 1, Sub: "a***@x.com",
		OK: true, Message: "空包",
	}); err != nil {
		t.Fatal(err)
	}
	rec, _ = d.GetGachaDraw("2026-08-25", 1)
	if rec.Drawn != "" || rec.Gifted || rec.Message != "空包" {
		t.Fatalf("upsert 未覆盖旧记录: %+v", rec)
	}

	_ = d.SaveGachaDraw(&GachaDrawRow{Day: "2026-08-25", Time: now, AccountID: 2, Sub: "b***@x.com", OK: true})
	rows, err := d.ListGachaDraws(10)
	if err != nil || len(rows) != 2 {
		t.Fatalf("列表数量错误: %d %v", len(rows), err)
	}
	if rows[0].AccountID != 2 {
		t.Fatalf("应按新→旧排序: %+v", rows[0])
	}

	if rec, _ := d.GetGachaDraw("2026-08-26", 1); rec != nil {
		t.Fatalf("不存在的记录应返回 nil: %+v", rec)
	}
}
