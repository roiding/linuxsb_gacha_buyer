// Package store 购买记录存储（SQLite）。
package store

import (
	"time"

	"gacha-buyer/internal/db"
	"gacha-buyer/internal/market"
)

// Purchase 购买记录（对外模型，与原 JSON 版兼容）。
type Purchase = db.PurchaseRow

// MarketSnapshot 最近一次市场扫描快照（不含 CSRF，重启后恢复展示）。
type MarketSnapshot struct {
	At       time.Time        `json:"at"`
	Listings []market.Listing `json:"listings"`
}

// SaveMarketSnapshot 持久化最近一次扫描结果。
func (s *Store) SaveMarketSnapshot(listings []market.Listing, at time.Time) {
	_ = s.d.SetJSON("market_snapshot", &MarketSnapshot{At: at, Listings: listings})
}

// LoadMarketSnapshot 读取持久化的快照；无记录返回 nil。
func (s *Store) LoadMarketSnapshot() *MarketSnapshot {
	var snap MarketSnapshot
	if err := s.d.GetJSON("market_snapshot", &snap); err != nil {
		return nil
	}
	return &snap
}

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
