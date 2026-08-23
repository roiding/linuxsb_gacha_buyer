package db

import "time"

// PurchaseRow purchases 表存储模型。
type PurchaseRow struct {
	Time      time.Time
	ListingID int
	Name      string
	Rarity    string
	Price     int
	Qty       int
	Cost      int
	DryRun    bool
	OK        bool
	Message   string
}

// AddPurchase 插入购买记录。
func (d *DB) AddPurchase(p *PurchaseRow) error {
	_, err := d.Exec(`INSERT INTO purchases(time, listing_id, name, rarity, price, qty, cost, dry_run, ok, message)
		VALUES(?,?,?,?,?,?,?,?,?,?)`,
		p.Time.UTC().Format(time.RFC3339), p.ListingID, p.Name, p.Rarity,
		p.Price, p.Qty, p.Cost, b2i(p.DryRun), b2i(p.OK), p.Message)
	return err
}

const purchaseCols = `time, listing_id, name, rarity, price, qty, cost, dry_run, ok, message`

// ListPurchases 新→旧分页。
func (d *DB) ListPurchases(limit int) ([]*PurchaseRow, error) {
	rows, err := d.Query(`SELECT `+purchaseCols+` FROM purchases ORDER BY id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*PurchaseRow
	for rows.Next() {
		p := &PurchaseRow{}
		var ts string
		var dry, ok int
		if err := rows.Scan(&ts, &p.ListingID, &p.Name, &p.Rarity, &p.Price, &p.Qty, &p.Cost, &dry, &ok, &p.Message); err != nil {
			return nil, err
		}
		p.Time, _ = time.Parse(time.RFC3339, ts)
		p.DryRun, p.OK = dry != 0, ok != 0
		out = append(out, p)
	}
	return out, rows.Err()
}

// PurchaseStats 成交统计（不含 dry-run 与失败）。
type PurchaseStats struct {
	TotalSpent int
	OKCount    int
}

// GetPurchaseStats 统计真实成交。
func (d *DB) GetPurchaseStats() (*PurchaseStats, error) {
	s := &PurchaseStats{}
	err := d.QueryRow(`SELECT COALESCE(SUM(cost),0), COUNT(*) FROM purchases WHERE ok=1 AND dry_run=0`).
		Scan(&s.TotalSpent, &s.OKCount)
	return s, err
}

// LastPurchaseAt 某 listing 最近一次尝试时间（去重用；无记录返回零值）。
func (d *DB) LastPurchaseAt(listingID int) time.Time {
	var ts string
	err := d.QueryRow(`SELECT time FROM purchases WHERE listing_id=? ORDER BY id DESC LIMIT 1`, listingID).Scan(&ts)
	if err != nil {
		return time.Time{}
	}
	t, _ := time.Parse(time.RFC3339, ts)
	return t
}

// TransferRow transfers 表存储模型。
type TransferRow struct {
	Time      time.Time
	AccountID int
	Sub       string
	CheckIn   bool
	Balance   int
	TipAmount int
	TopicID   int
	DryRun    bool
	OK        bool
	Message   string
}

// AddTransfer 插入归集记录。
func (d *DB) AddTransfer(t *TransferRow) error {
	_, err := d.Exec(`INSERT INTO transfers(time, account_id, sub, check_in, balance, tip_amount, topic_id, dry_run, ok, message)
		VALUES(?,?,?,?,?,?,?,?,?,?)`,
		t.Time.UTC().Format(time.RFC3339), t.AccountID, t.Sub, b2i(t.CheckIn), t.Balance,
		t.TipAmount, t.TopicID, b2i(t.DryRun), b2i(t.OK), t.Message)
	return err
}

// ListTransfers 新→旧分页。
func (d *DB) ListTransfers(limit int) ([]*TransferRow, error) {
	rows, err := d.Query(`SELECT time, account_id, sub, check_in, balance, tip_amount, topic_id, dry_run, ok, message
		FROM transfers ORDER BY id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*TransferRow
	for rows.Next() {
		t := &TransferRow{}
		var ts string
		var ci, dry, ok int
		if err := rows.Scan(&ts, &t.AccountID, &t.Sub, &ci, &t.Balance, &t.TipAmount, &t.TopicID, &dry, &ok, &t.Message); err != nil {
			return nil, err
		}
		t.Time, _ = time.Parse(time.RFC3339, ts)
		t.CheckIn, t.DryRun, t.OK = ci != 0, dry != 0, ok != 0
		out = append(out, t)
	}
	return out, rows.Err()
}

// TippedToday 某小号今日（本地时区）是否已成功打赏过。
func (d *DB) TippedToday(accountID int, now time.Time) bool {
	var ts string
	row := d.QueryRow(`SELECT time FROM transfers WHERE account_id=? AND ok=1 AND tip_amount>0 ORDER BY id DESC LIMIT 1`, accountID)
	if row.Scan(&ts) != nil {
		return false
	}
	t, err := time.Parse(time.RFC3339, ts)
	if err != nil {
		return false
	}
	y1, m1, dd1 := t.Local().Date()
	y2, m2, dd2 := now.Date()
	return y1 == y2 && m1 == m2 && dd1 == dd2
}
