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

// New 创建引擎。
func New(cfg *config.Config, st *store.Store, logf func(string, ...any)) *Engine {
	if logf == nil {
		logf = log.Printf
	}
	return &Engine{cfg: cfg, st: st, logf: logf}
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
	MinBalance int               `json:"min_balance"`
	ScanCount  int               `json:"scan_count"`
	BuyOK      int               `json:"buy_ok"`
	LastScanAt string            `json:"last_scan_at"`
	LastError  string            `json:"last_error"`
	Listings   []market.Listing  `json:"listings"`
	Rules      config.PriceRules `json:"rules"`
	SSRPrices  map[string]int    `json:"ssr_prices"`
}

// Snapshot 返回当前状态（不触发网络）。
func (e *Engine) Snapshot() Status {
	e.mu.Lock()
	defer e.mu.Unlock()
	s := Status{
		Running:    e.running,
		DryRun:     e.cfg.DryRun,
		Points:     e.points,
		MinBalance: e.cfg.MinBalance,
		ScanCount:  e.scanCount,
		BuyOK:      e.buyCount,
		Listings:   e.listings,
		Rules:      e.cfg.Rules,
		SSRPrices:  e.cfg.SSRPrices,
		LastError:  e.lastErr,
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

// match 按限价规则筛选可买 listing；数量按在售余量全部尝试。
func (e *Engine) match(all []market.Listing) []market.Listing {
	var out []market.Listing
	for _, l := range all {
		limit := limitFor(e.cfg, l)
		if limit <= 0 || l.Price > limit || l.Remain <= 0 {
			continue
		}
		out = append(out, l)
	}
	return out
}

func limitFor(cfg *config.Config, l market.Listing) int {
	if l.Rarity == market.SSR {
		return cfg.SSRPrices[l.Name]
	}
	switch l.Rarity {
	case market.SR:
		return cfg.Rules.SR
	case market.R:
		return cfg.Rules.R
	case market.N:
		return cfg.Rules.N
	case market.UR:
		return cfg.Rules.UR
	}
	return 0
}

// buyOne 执行一次购买并记录。
func (e *Engine) buyOne(client *site.Client, l market.Listing) {
	qty := l.Remain
	cost := qty * l.Price

	dup := e.st.LastAttemptAt(l.ListingID)
	if time.Since(dup) < 10*time.Minute {
		return // 同一 listing 十分钟内不重复尝试
	}

	e.mu.Lock()
	minBalance, dryRun := e.cfg.MinBalance, e.cfg.DryRun
	e.mu.Unlock()
	if !dryRun {
		points, err := client.FetchPoints()
		if err != nil {
			e.logf("获取余额失败，跳过 %s%s：%v", l.Emoji, l.Name, err)
			return
		}
		e.mu.Lock()
		e.points = points
		e.mu.Unlock()
		if points-cost < minBalance {
			e.logf("余额保护线拦截 %s%s ×%d：余额 %d，需保留 %d", l.Emoji, l.Name, qty, points, minBalance)
			return
		}
	}

	res := site.BuyResult{Message: "dry-run 跳过下单"}
	if dryRun {
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
		DryRun:    dryRun,
		OK:        res.OK || dryRun,
		Message:   res.Message,
	}
	if err := e.st.Add(rec); err != nil {
		e.logf("记录落盘失败: %v", err)
	}
	if res.OK && !dryRun {
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
