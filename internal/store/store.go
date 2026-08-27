// Package store 购买记录存储（SQLite）。
package store

import (
	"time"

	"gacha-buyer/internal/db"
)

// Purchase 购买记录（对外模型，与原 JSON 版兼容）。
type Purchase = db.PurchaseRow

// Store 基于 SQLite 的记录存储。
type Store struct {
	d *db.DB
}

// New 创建。
func New(d *db.DB) *Store { return &Store{d: d} }

// DB 暴露底层连接（供 collector 等复用同一实例）。
func (s *Store) DB() *db.DB { return s.d }

// Add 追加购买记录。
func (s *Store) Add(p Purchase) error { return s.d.AddPurchase(&p) }

// All 全部记录（新→旧）。
func (s *Store) All() []Purchase {
	rows, err := s.d.ListPurchases(1000)
	if err != nil {
		return nil
	}
	out := make([]Purchase, len(rows))
	for i, r := range rows {
		out[i] = *r
	}
	return out
}

// PurchasesPage 分页返回购买记录（新→旧，offset 从 0 开始）。
func (s *Store) PurchasesPage(offset, limit int) []Purchase {
	rows, err := s.d.ListPurchasesPage(offset, limit)
	if err != nil {
		return nil
	}
	out := make([]Purchase, len(rows))
	for i, r := range rows {
		out[i] = *r
	}
	return out
}

// CountPurchases 购买记录总数。
func (s *Store) CountPurchases() int { return s.d.CountPurchases() }

// TotalSpent 真实成交总花费。
func (s *Store) TotalSpent() int {
	st, err := s.d.GetPurchaseStats()
	if err != nil {
		return 0
	}
	return st.TotalSpent
}

// CountOK 成交笔数。
func (s *Store) CountOK() int {
	st, err := s.d.GetPurchaseStats()
	if err != nil {
		return 0
	}
	return st.OKCount
}

// LastAttemptAt 某 listing 最近尝试时间。
func (s *Store) LastAttemptAt(listingID int) time.Time { return s.d.LastPurchaseAt(listingID) }
