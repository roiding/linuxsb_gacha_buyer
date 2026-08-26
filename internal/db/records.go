package db

import (
	"database/sql"
	"errors"
	"time"
)

// PurchaseRow purchases 表存储模型。
type PurchaseRow struct {
	Time      time.Time `json:"time"`
	ListingID int       `json:"listing_id"`
	Name      string    `json:"name"`
	Rarity    string    `json:"rarity"`
	Price     int       `json:"price"`
	Qty       int       `json:"qty"`
	Cost      int       `json:"cost"`
	DryRun    bool      `json:"dry_run"`
	OK        bool      `json:"ok"`
	Submitted bool      `json:"submitted"`
	Confirmed bool      `json:"confirmed"`
	Message   string    `json:"message"`
}

// AddPurchase 插入购买记录。
func (d *DB) AddPurchase(p *PurchaseRow) error {
	_, err := d.Exec(`INSERT INTO purchases(time, listing_id, name, rarity, price, qty, cost, dry_run, ok, submitted, confirmed, message)
		VALUES(?,?,?,?,?,?,?,?,?,?,?,?)`,
		p.Time.UTC().Format(time.RFC3339), p.ListingID, p.Name, p.Rarity,
		p.Price, p.Qty, p.Cost, b2i(p.DryRun), b2i(p.OK), b2i(p.Submitted), b2i(p.Confirmed), p.Message)
	return err
}

const purchaseCols = `time, listing_id, name, rarity, price, qty, cost, dry_run, ok, submitted, confirmed, message`

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
		var dry, ok, submitted, confirmed int
		if err := rows.Scan(&ts, &p.ListingID, &p.Name, &p.Rarity, &p.Price, &p.Qty, &p.Cost, &dry, &ok, &submitted, &confirmed, &p.Message); err != nil {
			return nil, err
		}
		p.Time, _ = time.Parse(time.RFC3339, ts)
		p.DryRun, p.OK = dry != 0, ok != 0
		p.Submitted, p.Confirmed = submitted != 0, confirmed != 0
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
	err := d.QueryRow(`SELECT COALESCE(SUM(cost),0), COUNT(*) FROM purchases WHERE confirmed=1 AND dry_run=0`).
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

type TransferRow struct {
	RecordID      int64
	Time          time.Time
	AccountID     int
	Sub           string
	CheckIn       bool
	Balance       int
	BalanceBefore int
	BalanceAfter  int
	TipAmount     int
	TopicID       int
	DryRun        bool
	OK            bool
	Submitted     bool
	Confirmed     bool
	Pending       bool
	Retryable     bool
	HTTP          int
	Attempt       int
	Message       string
}

// AddTransfer 插入归集记录。
func (d *DB) AddTransfer(t *TransferRow) error {
	_, err := d.Exec(`INSERT INTO transfers(time, account_id, sub, check_in, balance, balance_before, balance_after, tip_amount, topic_id, dry_run, ok, submitted, confirmed, pending, retryable, http_status, attempt, message)
		VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		t.Time.UTC().Format(time.RFC3339), t.AccountID, t.Sub, b2i(t.CheckIn), t.Balance,
		t.BalanceBefore, t.BalanceAfter, t.TipAmount, t.TopicID, b2i(t.DryRun), b2i(t.OK),
		b2i(t.Submitted), b2i(t.Confirmed), b2i(t.Pending), b2i(t.Retryable), t.HTTP, t.Attempt, t.Message)
	return err
}

// UpdateTransfer 更新一条归集记录，供提交后立即写入确认结果。
func (d *DB) UpdateTransfer(id int64, t *TransferRow) error {
	_, err := d.Exec(`UPDATE transfers SET time=?, account_id=?, sub=?, check_in=?, balance=?, balance_before=?, balance_after=?, tip_amount=?, topic_id=?, dry_run=?, ok=?, submitted=?, confirmed=?, pending=?, retryable=?, http_status=?, attempt=?, message=? WHERE id=?`,
		t.Time.UTC().Format(time.RFC3339), t.AccountID, t.Sub, b2i(t.CheckIn), t.Balance,
		t.BalanceBefore, t.BalanceAfter, t.TipAmount, t.TopicID, b2i(t.DryRun), b2i(t.OK),
		b2i(t.Submitted), b2i(t.Confirmed), b2i(t.Pending), b2i(t.Retryable), t.HTTP, t.Attempt, t.Message, id)
	return err
}

// AddTransferPending 预写一条归集记录并返回其 ID。
func (d *DB) AddTransferPending(t *TransferRow) (int64, error) {
	result, err := d.Exec(`INSERT INTO transfers(time, account_id, sub, check_in, balance, balance_before, balance_after, tip_amount, topic_id, dry_run, ok, submitted, confirmed, pending, retryable, http_status, attempt, message)
		VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		t.Time.UTC().Format(time.RFC3339), t.AccountID, t.Sub, b2i(t.CheckIn), t.Balance,
		t.BalanceBefore, t.BalanceAfter, t.TipAmount, t.TopicID, b2i(t.DryRun), b2i(t.OK),
		b2i(t.Submitted), b2i(t.Confirmed), b2i(t.Pending), b2i(t.Retryable), t.HTTP, t.Attempt, t.Message)
	if err != nil {
		return 0, err
	}
	return result.LastInsertId()
}

// ListTransfers 新→旧分页。
func (d *DB) ListTransfers(limit int) ([]*TransferRow, error) {
	rows, err := d.Query(`SELECT time, account_id, sub, check_in, balance, balance_before, balance_after, tip_amount, topic_id, dry_run, ok, submitted, confirmed, pending, retryable, http_status, attempt, message
		FROM transfers ORDER BY id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*TransferRow
	for rows.Next() {
		t := &TransferRow{}
		var ts string
		var ci, dry, ok, submitted, confirmed, pending, retryable int
		if err := rows.Scan(&ts, &t.AccountID, &t.Sub, &ci, &t.Balance, &t.BalanceBefore, &t.BalanceAfter, &t.TipAmount, &t.TopicID, &dry, &ok, &submitted, &confirmed, &pending, &retryable, &t.HTTP, &t.Attempt, &t.Message); err != nil {
			return nil, err
		}
		t.Time, _ = time.Parse(time.RFC3339, ts)
		t.CheckIn, t.DryRun, t.OK = ci != 0, dry != 0, ok != 0
		t.Submitted, t.Confirmed, t.Pending, t.Retryable = submitted != 0, confirmed != 0, pending != 0, retryable != 0
		out = append(out, t)
	}
	return out, rows.Err()
}

// TippedToday 某小号今日是否已通过业务核验成功打赏过。
func (d *DB) TippedToday(accountID int, now time.Time) bool {
	var ts string
	row := d.QueryRow(`SELECT time FROM transfers WHERE account_id=? AND ok=1 AND confirmed=1 AND dry_run=0 AND tip_amount>0 ORDER BY id DESC LIMIT 1`, accountID)
	if row.Scan(&ts) != nil {
		return false
	}
	t, err := time.Parse(time.RFC3339, ts)
	if err != nil {
		return false
	}
	y1, m1, dd1 := t.Local().Date()
	y2, m2, dd2 := now.Local().Date()
	return y1 == y2 && m1 == m2 && dd1 == dd2
}

// TransferHandledToday 判断当天是否已有经过新确认语义的处理结果，包括无需归集。
func (d *DB) TransferHandledToday(accountID int, now time.Time) bool {
	start := time.Date(now.Local().Year(), now.Local().Month(), now.Local().Day(), 0, 0, 0, 0, now.Location())
	end := start.AddDate(0, 0, 1)
	var n int
	err := d.QueryRow(`SELECT COUNT(*) FROM transfers WHERE account_id=? AND confirmed=1 AND dry_run=0 AND time>=? AND time<?`,
		accountID, start.UTC().Format(time.RFC3339), end.UTC().Format(time.RFC3339)).Scan(&n)
	return err == nil && n > 0
}

// TransferPendingToday 保留兼容名称；未解决的未知结果会跨日阻止重复提交。
func (d *DB) TransferPendingToday(accountID int, now time.Time) bool {
	_, err := d.GetPendingTransfer(accountID, now)
	return err == nil
}

// GetPendingTransfer 读取指定账号最新的未解决待核验记录。
func (d *DB) GetPendingTransfer(accountID int, _ time.Time) (*TransferRow, error) {
	row := d.QueryRow(`SELECT id, time, account_id, sub, check_in, balance, balance_before, balance_after, tip_amount, topic_id, dry_run, ok, submitted, confirmed, pending, retryable, http_status, attempt, message
		FROM transfers WHERE account_id=? AND pending=1 AND dry_run=0 ORDER BY id DESC LIMIT 1`, accountID)
	var id int64
	var t TransferRow
	var ts string
	var ci, dry, ok, submitted, confirmed, pending, retryable int
	if err := row.Scan(&id, &ts, &t.AccountID, &t.Sub, &ci, &t.Balance, &t.BalanceBefore, &t.BalanceAfter, &t.TipAmount, &t.TopicID, &dry, &ok, &submitted, &confirmed, &pending, &retryable, &t.HTTP, &t.Attempt, &t.Message); err != nil {
		return nil, err
	}
	t.Time, _ = time.Parse(time.RFC3339, ts)
	t.CheckIn, t.DryRun, t.OK = ci != 0, dry != 0, ok != 0
	t.Submitted, t.Confirmed, t.Pending, t.Retryable = submitted != 0, confirmed != 0, pending != 0, retryable != 0
	t.RecordID = id
	return &t, nil
}

// CollectorSchedule 每日归集计划，按“天+小号”一行。
type CollectorSchedule struct {
	Day         string    `json:"day"`
	Account     string    `json:"account"`
	PlannedAt   time.Time `json:"planned_at"`
	StartedAt   time.Time `json:"started_at,omitempty"`
	CompletedAt time.Time `json:"completed_at,omitempty"`
	Status      string    `json:"status"`
	Retries     int       `json:"retries,omitempty"`
}

// GetCollectorSchedules 读取指定自然日全部小号的归集计划。
func (d *DB) GetCollectorSchedules(day string) ([]*CollectorSchedule, error) {
	rows, err := d.Query(`SELECT day, account, planned_at, started_at, completed_at, status, retries FROM collector_schedule WHERE day=?`, day)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*CollectorSchedule
	for rows.Next() {
		s := &CollectorSchedule{}
		var planned, started, completed string
		if err := rows.Scan(&s.Day, &s.Account, &planned, &started, &completed, &s.Status, &s.Retries); err != nil {
			return nil, err
		}
		s.PlannedAt, _ = time.Parse(time.RFC3339, planned)
		if started != "" {
			s.StartedAt, _ = time.Parse(time.RFC3339, started)
		}
		if completed != "" {
			s.CompletedAt, _ = time.Parse(time.RFC3339, completed)
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// SaveCollectorSchedule 保存某个小号当天的归集计划。
func (d *DB) SaveCollectorSchedule(s *CollectorSchedule) error {
	_, err := d.Exec(`INSERT INTO collector_schedule(day, account, planned_at, started_at, completed_at, status, retries)
		VALUES(?,?,?,?,?,?,?) ON CONFLICT(day, account) DO UPDATE SET planned_at=excluded.planned_at, started_at=excluded.started_at, completed_at=excluded.completed_at, status=excluded.status, retries=excluded.retries`,
		s.Day, s.Account, s.PlannedAt.UTC().Format(time.RFC3339), formatTime(s.StartedAt), formatTime(s.CompletedAt), s.Status, s.Retries)
	return err
}

// DeleteCollectorSchedules 删除当天尚未执行的计划，用于配置变更重算。
func (d *DB) DeleteCollectorSchedules(day string) error {
	_, err := d.Exec(`DELETE FROM collector_schedule WHERE day=? AND status='planned'`, day)
	return err
}

func formatTime(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339)
}

type LotteryReplyRow struct {
	Time      time.Time `json:"time"`
	AccountID int       `json:"-"`
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

// AddLotteryReply 插入抽奖回复记录。
func (d *DB) AddLotteryReply(r *LotteryReplyRow) error {
	_, err := d.Exec(`INSERT INTO lottery_replies(time, account_id, sub, topic_id, content, captcha, dry_run, submitted, confirmed, reply_id, message)
		VALUES(?,?,?,?,?,?,?,?,?,?,?)`, r.Time.UTC().Format(time.RFC3339), r.AccountID, r.Sub, r.TopicID,
		r.Content, b2i(r.Captcha), b2i(r.DryRun), b2i(r.Submitted), b2i(r.Confirmed), r.ReplyID, r.Message)
	return err
}

// ListLotteryReplies 新→旧分页。
func (d *DB) ListLotteryReplies(limit int) ([]*LotteryReplyRow, error) {
	rows, err := d.Query(`SELECT time, account_id, sub, topic_id, content, captcha, dry_run, submitted, confirmed, reply_id, message
		FROM lottery_replies ORDER BY id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*LotteryReplyRow
	for rows.Next() {
		r := &LotteryReplyRow{}
		var ts string
		var captcha, dry, submitted, confirmed int
		if err := rows.Scan(&ts, &r.AccountID, &r.Sub, &r.TopicID, &r.Content, &captcha, &dry, &submitted, &confirmed, &r.ReplyID, &r.Message); err != nil {
			return nil, err
		}
		r.Time, _ = time.Parse(time.RFC3339, ts)
		r.Captcha, r.DryRun = captcha != 0, dry != 0
		r.Submitted, r.Confirmed = submitted != 0, confirmed != 0
		out = append(out, r)
	}
	return out, rows.Err()
}

// LotteryReplyConfirmed 判断某账号是否已确认回复过该主题。
func (d *DB) LotteryReplyConfirmed(accountID, topicID int) bool {
	var n int
	err := d.QueryRow(`SELECT COUNT(*) FROM lottery_replies WHERE account_id=? AND topic_id=? AND confirmed=1 AND dry_run=0`, accountID, topicID).Scan(&n)
	return err == nil && n > 0
}

// LotteryReplyPendingRecently 判断某账号近期是否有已提交但未确认的回复。
func (d *DB) LotteryReplyPendingRecently(accountID, topicID int, since time.Time) bool {
	var n int
	err := d.QueryRow(`SELECT COUNT(*) FROM lottery_replies WHERE account_id=? AND topic_id=? AND submitted=1 AND confirmed=0 AND dry_run=0 AND time>=?`,
		accountID, topicID, since.UTC().Format(time.RFC3339)).Scan(&n)
	return err == nil && n > 0
}

// GachaDrawRow gacha_draws 表存储模型：某账号某天每日免费一抽的结果。
type GachaDrawRow struct {
	ID         int64     `json:"-"`
	Day        string    `json:"day"`
	Time       time.Time `json:"time"`
	AccountID  int       `json:"-"`
	Sub        string    `json:"sub"`   // 已脱敏
	Drawn      string    `json:"drawn"` // 抽到的称号名；空=空包/未识别
	OK         bool      `json:"ok"`    // 今日抽卡是否已落定（不重复抽）
	Gifted     bool      `json:"gifted"`
	GiftTarget string    `json:"gift_target"`
	Message    string    `json:"message"`
}

// SaveGachaDraw 按 (day, account_id) 写入或更新一条抽卡记录。
func (d *DB) SaveGachaDraw(r *GachaDrawRow) error {
	_, err := d.Exec(`INSERT INTO gacha_draws(day, time, account_id, sub, drawn, ok, gifted, gift_target, message)
		VALUES(?,?,?,?,?,?,?,?,?)
		ON CONFLICT(day, account_id) DO UPDATE SET
			time=excluded.time, sub=excluded.sub, drawn=excluded.drawn, ok=excluded.ok,
			gifted=excluded.gifted, gift_target=excluded.gift_target, message=excluded.message`,
		r.Day, r.Time.UTC().Format(time.RFC3339), r.AccountID, r.Sub, r.Drawn,
		b2i(r.OK), b2i(r.Gifted), r.GiftTarget, r.Message)
	return err
}

// GetGachaDraw 读取某账号某天的抽卡记录；不存在时返回 (nil, nil)。
func (d *DB) GetGachaDraw(day string, accountID int) (*GachaDrawRow, error) {
	r := &GachaDrawRow{}
	var ts string
	var ok, gifted int
	err := d.QueryRow(`SELECT id, day, time, account_id, sub, drawn, ok, gifted, gift_target, message
		FROM gacha_draws WHERE day=? AND account_id=?`, day, accountID).
		Scan(&r.ID, &r.Day, &ts, &r.AccountID, &r.Sub, &r.Drawn, &ok, &gifted, &r.GiftTarget, &r.Message)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	r.Time, _ = time.Parse(time.RFC3339, ts)
	r.OK, r.Gifted = ok != 0, gifted != 0
	return r, nil
}

// ListGachaDraws 新→旧返回最近抽卡记录。
func (d *DB) ListGachaDraws(limit int) ([]*GachaDrawRow, error) {
	rows, err := d.Query(`SELECT id, day, time, account_id, sub, drawn, ok, gifted, gift_target, message
		FROM gacha_draws ORDER BY id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]*GachaDrawRow, 0)
	for rows.Next() {
		r := &GachaDrawRow{}
		var ts string
		var ok, gifted int
		if err := rows.Scan(&r.ID, &r.Day, &ts, &r.AccountID, &r.Sub, &r.Drawn, &ok, &gifted, &r.GiftTarget, &r.Message); err != nil {
			return nil, err
		}
		r.Time, _ = time.Parse(time.RFC3339, ts)
		r.OK, r.Gifted = ok != 0, gifted != 0
		out = append(out, r)
	}
	return out, rows.Err()
}
