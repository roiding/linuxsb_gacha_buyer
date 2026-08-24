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
