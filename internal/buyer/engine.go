// Package buyer 收购引擎：按限价规则扫描市场并自动下单。
package buyer

import (
	"fmt"
	"log"
	"math/rand"
	"strings"
	"sync"
	"time"

	"gacha-buyer/internal/accounts"
	"gacha-buyer/internal/config"
	"gacha-buyer/internal/market"
	"gacha-buyer/internal/site"
	"gacha-buyer/internal/store"
)

// Engine 常驻收购引擎。
type Engine struct {
	cfg  *config.Config
	st   *store.Store
	logf func(string, ...any)
	Mgr  *accounts.Manager // 可选：注入后复用主号会话

	mu         sync.Mutex
	running    bool
	stopCh     chan struct{}
	loopDone   chan struct{}
	lastScanAt time.Time
	lastErr    string
	listings   []market.Listing // 最近一次扫描快照
	buyCount   int
	scanCount  int
	points     int
	loggedIn   bool

	client *site.Client
}

// New 创建引擎；恢复上次持久化的市场快照（重启后在售快照仍可见）。
func New(cfg *config.Config, st *store.Store, logf func(string, ...any)) *Engine {
	if logf == nil {
		logf = log.Printf
	}
	e := &Engine{cfg: cfg, st: st, logf: logf}
	if snap := st.LoadMarketSnapshot(); snap != nil && len(snap.Listings) > 0 {
		e.listings = snap.Listings
		e.lastScanAt = snap.At
	}
	return e
}

// Running 是否在运行。
func (e *Engine) Running() bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.running
}

// Start 启动扫描循环（已运行则幂等返回）。
func (e *Engine) Start() error {
	e.mu.Lock()
	if e.running {
		e.mu.Unlock()
		return nil
	}
	e.running = true
	e.stopCh = make(chan struct{})
	e.loopDone = make(chan struct{})
	e.mu.Unlock()

	go e.loop()
	e.logf("收购引擎已启动 (间隔 %ds, dry_run=%v)", e.cfg.ScanSec, e.cfg.DryRun)
	return nil
}

// Stop 停止循环并等待退出。
func (e *Engine) Stop() {
	e.mu.Lock()
	if !e.running {
		e.mu.Unlock()
		return
	}
	ch := e.stopCh
	done := e.loopDone
	e.running = false
	e.mu.Unlock()
	close(ch)
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		e.logf("警告：引擎循环未在超时内退出")
	}
	e.logf("收购引擎已停止")
}

// ScanNow 立即触发一轮扫描（阻塞直至完成）。
func (e *Engine) ScanNow() error {
	if err := e.ensureClient(e.cfg); err != nil {
		return err
	}
	e.scanOnce()
	return nil
}

// InvalidateSession 配置变更后强制下次重新登录。
func (e *Engine) InvalidateSession() {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.loggedIn = false
}

func (e *Engine) loop() {
	defer close(e.loopDone)
	// 启动立即扫一轮
	for {
		e.scanOnce()
		e.mu.Lock()
		interval := time.Duration(e.cfg.ScanSec) * time.Second
		e.mu.Unlock()
		// 随机抖动 ±15%，避免机械请求特征
		jitter := time.Duration(float64(interval) * (0.85 + 0.3*rand.Float64()))
		select {
		case <-e.stopCh:
			return
		case <-time.After(jitter):
		}
	}
}

// Status 给 Web 展示的运行状态。
type Status struct {
	Running    bool              `json:"running"`
	LoggedIn   bool              `json:"logged_in"`
	DryRun     bool              `json:"dry_run"`
	Points     int               `json:"points"`
	BudgetUsed int               `json:"budget_used"`
	BudgetLeft int               `json:"budget_left"`
	ScanCount  int               `json:"scan_count"`
	BuyOK      int               `json:"buy_ok"`
	LastScanAt string            `json:"last_scan_at"`
	LastError  string            `json:"last_error"`
	Listings   []market.Listing  `json:"listings"`
	Rules      config.PriceRules `json:"rules"`
	MaxSpend   int               `json:"max_spend"`
}

// Snapshot 返回当前状态（不触发网络）。
func (e *Engine) Snapshot() Status {
	e.mu.Lock()
	defer e.mu.Unlock()
	s := Status{
		Running:    e.running,
		DryRun:     e.cfg.DryRun,
		Points:     e.points,
		BudgetUsed: e.st.TotalSpent(),
		ScanCount:  e.scanCount,
		BuyOK:      e.buyCount,
		Listings:   e.listings,
		Rules:      e.cfg.Rules,
		MaxSpend:   e.cfg.MaxSpend,
		LastError:  e.lastErr,
	}
	s.BudgetLeft = e.cfg.MaxSpend - s.BudgetUsed
	if s.BudgetLeft < 0 {
		s.BudgetLeft = 0
	}
	if !e.lastScanAt.IsZero() {
		s.LastScanAt = e.lastScanAt.Format("2006-01-02 15:04:05")
	}
	s.LoggedIn = e.client != nil && e.loggedIn
	return s
}

// scanOnce 单轮：登录保活 → 抓市场 → 匹配 → 下单。
func (e *Engine) scanOnce() {
	e.mu.Lock()
	cfg := *e.cfg
	client := e.client
	e.mu.Unlock()

	if err := e.ensureClient(&cfg); err != nil {
		e.recordError(err)
		return
	}
	e.mu.Lock()
	client = e.client
	e.mu.Unlock()

	listings, err := client.FetchMarket()
	if err != nil {
		// 会话失效自动重登一次
		if isSessionLost(err) {
			e.logf("会话失效，重新登录…")
			if lerr := client.Login(); lerr != nil {
				err = fmt.Errorf("重登失败: %w", lerr)
			} else if listings, err = client.FetchMarket(); err != nil {
				err = fmt.Errorf("重抓市场失败: %w", err)
			}
		}
		if err != nil {
			e.recordError(err)
			return
		}
	}
	e.mu.Lock()
	e.listings = listings
	e.lastScanAt = time.Now()
	e.scanCount++
	e.mu.Unlock()
	e.st.SaveMarketSnapshot(listings, e.lastScanAt)

	// 刷新积分与登录态展示
	if pts, perr := client.FetchPoints(); perr == nil {
		e.mu.Lock()
		e.points = pts
		e.loggedIn = true
		e.mu.Unlock()
	}

	matches := e.match(listings)
	if len(matches) == 0 {
		e.recordError(nil)
		return
	}
	for _, m := range matches {
		select {
		case <-e.stopped():
			return
		default:
		}
		e.buyOne(client, m)
	}
	e.recordError(nil)
}

func (e *Engine) stopped() <-chan struct{} {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.stopCh
}

func isSessionLost(err error) bool {
	return err != nil && strings.Contains(err.Error(), "会话已失效")
}

// match 按限价规则筛选可买 listing，并做预算与数量裁剪。
func (e *Engine) match(all []market.Listing) []market.Listing {
	var out []market.Listing
	spent := e.st.TotalSpent()
	budgetLeft := e.cfg.MaxSpend - spent
	if budgetLeft <= 0 {
		return nil
	}
	nListings := 0
	for _, l := range all {
		limit := limitFor(e.cfg.Rules, l.Rarity)
		if limit <= 0 || l.Price > limit || l.Remain <= 0 {
			continue
		}
		if nListings >= e.cfg.MaxListings {
			break
		}
		qty := l.Remain
		if qty > e.cfg.MaxBuyOnce {
			qty = e.cfg.MaxBuyOnce
		}
		for qty > 0 && qty*l.Price > budgetLeft {
			qty--
		}
		if qty <= 0 {
			continue
		}
		out = append(out, market.Listing{
			ListingID: l.ListingID,
			Name:      l.Name,
			Emoji:     l.Emoji,
			Rarity:    l.Rarity,
			Price:     l.Price,
			CSRF:      l.CSRF,
			Remain:    qty, // 复用字段携带本次购买量
		})
		budgetLeft -= qty * l.Price
		nListings++
	}
	return out
}

func limitFor(r config.PriceRules, rarity market.Rarity) int {
	switch rarity {
	case market.SR:
		return r.SR
	case market.R:
		return r.R
	case market.N:
		return r.N
	case market.SSR:
		return r.SSR
	case market.UR:
		return r.UR
	}
	return 0
}

// buyOne 执行一次购买并记录。
func (e *Engine) buyOne(client *site.Client, l market.Listing) {
	qty := l.Remain // match() 已把购买量放进 Remain
	cost := qty * l.Price

	dup := e.st.LastAttemptAt(l.ListingID)
	if time.Since(dup) < 10*time.Minute {
		return // 同一 listing 十分钟内不重复尝试
	}

	res := site.BuyResult{Message: "dry-run 跳过下单"}
	if e.cfg.DryRun {
		e.logf("[DRY] 将购买 %s%s ×%d @%d = %d 分", l.Emoji, l.Name, qty, l.Price, cost)
	} else {
		res = client.Buy(l, qty)
		e.logf("购买 %s%s ×%d @%d → ok=%v %s", l.Emoji, l.Name, qty, l.Price, res.OK, res.Message)
	}

	rec := store.Purchase{
		Time:      time.Now(),
		ListingID: l.ListingID,
		Name:      l.Name,
		Rarity:    string(l.Rarity),
		Price:     l.Price,
		Qty:       qty,
		Cost:      cost,
		DryRun:    e.cfg.DryRun,
		OK:        res.OK || e.cfg.DryRun,
		Message:   res.Message,
	}
	if err := e.st.Add(rec); err != nil {
		e.logf("记录落盘失败: %v", err)
	}
	if res.OK && !e.cfg.DryRun {
		e.mu.Lock()
		e.buyCount++
		e.mu.Unlock()
	}
}

// ensureClient 惰性创建客户端并在需要时登录。注入了 Manager 时完全复用主号会话。
func (e *Engine) ensureClient(cfg *config.Config) error {
	e.mu.Lock()
	client := e.client
	loggedIn := e.loggedIn
	e.mu.Unlock()
	if client != nil && loggedIn {
		return nil
	}
	if e.Mgr != nil {
		c, a, err := e.Mgr.Main()
		if err != nil {
			return fmt.Errorf("主号会话不可用: %w", err)
		}
		e.mu.Lock()
		e.client = c
		e.loggedIn = a.Status == "ok"
		e.mu.Unlock()
		return nil
	}
	// 独立模式（无 Manager）
	e.mu.Lock()
	needNew := e.client == nil
	e.mu.Unlock()
	if needNew {
		c, err := site.NewClient(cfg, e.logf)
		if err != nil {
			return err
		}
		e.mu.Lock()
		e.client = c
		e.mu.Unlock()
	}
	e.mu.Lock()
	client = e.client
	e.mu.Unlock()
	if !loggedIn {
		if err := client.Login(); err != nil {
			return fmt.Errorf("登录失败: %w", err)
		}
		e.mu.Lock()
		e.loggedIn = true
		e.mu.Unlock()
	}
	return nil
}

func (e *Engine) recordError(err error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if err != nil {
		e.lastErr = err.Error()
	} else {
		e.lastErr = ""
	}
}
