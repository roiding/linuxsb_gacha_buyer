package db

import (
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

	schedule := &CollectorSchedule{Day: "2026-08-23", PlannedAt: now, Status: "planned"}
	if err := d.SaveCollectorSchedule(schedule); err != nil {
		t.Fatal(err)
	}
	got, err := d.GetCollectorSchedule(schedule.Day)
	if err != nil || got.Status != "planned" || got.PlannedAt.IsZero() {
		t.Fatalf("计划读取错误: %+v %v", got, err)
	}
	if err := d.DeleteCollectorSchedule(schedule.Day); err != nil {
		t.Fatal(err)
	}
	if _, err := d.GetCollectorSchedule(schedule.Day); err == nil {
		t.Fatal("删除计划后不应继续读取到记录")
	}
}
