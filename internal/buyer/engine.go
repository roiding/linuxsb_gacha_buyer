// Package buyer 收购引擎：按限价规则扫描市场并自动下单。
package buyer

import (
	"fmt"
	"log"
	"math/rand"
	"sort"
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

	// scanMu 串行化常规扫描（scanOnce）与配置变更后的分类深度收购（deepScan），
	// 避免两个流程并发对同一挂牌下单。
	scanMu sync.Mutex

	// owned 背包持有数缓存（称号名 → 数量），配合定向收购的 Max 限制使用；
	// 每轮扫描开始时从 /gacha_profile 刷新。
	owned map[string]int

	// titleRarity 称号名 → 稀有度目录缓存。定向规则推算扫描分类要用目录页，
	// 但目录极少变化：全部定向名都能命中缓存时就不再请求目录页。
	titleRarity map[string]market.Rarity

	client *site.Client
}

// New 创建引擎。
func New(cfg *config.Config, st *store.Store, logf func(string, ...any)) *Engine {
	if logf == nil {
		logf = log.Printf
	}
	return &Engine{cfg: cfg, st: st, logf: logf, owned: make(map[string]int)}
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
	e.logf("收购引擎已启动 (间隔 %ds, 扫描=%s, dry_run=%v)", e.cfg.ScanSec, e.cfg.EffectiveScanMode(), e.cfg.DryRun)
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
	Running    bool                         `json:"running"`
	LoggedIn   bool                         `json:"logged_in"`
	DryRun     bool                         `json:"dry_run"`
	Points     int                          `json:"points"`
	MinBalance int                          `json:"min_balance"`
	ScanCount  int                          `json:"scan_count"`
	BuyOK      int                          `json:"buy_ok"`
	LastScanAt string                       `json:"last_scan_at"`
	LastError  string                       `json:"last_error"`
	Listings   []market.Listing             `json:"listings"`
	Rules      config.PriceRules            `json:"rules"`
	SSRPrices  map[string]int               `json:"ssr_prices"`
	Targets    map[string]config.TargetRule `json:"targets"`
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
		Targets:    e.cfg.Targets,
		LastError:  e.lastErr,
	}
	if !e.lastScanAt.IsZero() {
		s.LastScanAt = e.lastScanAt.Format("2006-01-02 15:04:05")
	}
	s.LoggedIn = e.client != nil && e.loggedIn
	return s
}

// scanOnce 单轮：登录保活 → 按配置的扫描方式抓取候选（fast 只扫默认页 1 请求；
// thorough 按分类价格升序翻页，页内超限即停）→ 匹配 → 下单。
func (e *Engine) scanOnce() {
	e.scanMu.Lock()
	defer e.scanMu.Unlock()

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

	listings, err := e.fetchBuyables(client, &cfg)
	if err != nil {
		// 会话失效自动重登一次
		if isSessionLost(err) {
			e.logf("会话失效，重新登录…")
			if lerr := client.Login(); lerr != nil {
				err = fmt.Errorf("重登失败: %w", lerr)
			} else if listings, err = e.fetchBuyables(client, &cfg); err != nil {
				err = fmt.Errorf("重扫市场失败: %w", err)
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
	// 刷新背包持有数缓存（仅当存在带数量上限的定向规则）
	e.refreshOwned(client)

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

// DeepScanNow 立即按采购配置启用的稀有度分类（N/R/SR/UR/SSR）以价格升序分页扫全市场并收购，
// 覆盖第一页之外的便宜挂牌。先确保会话可用，扫描在后台执行。
func (e *Engine) DeepScanNow() error {
	if err := e.ensureClient(e.cfg); err != nil {
		return err
	}
	go e.deepScan()
	return nil
}

func (e *Engine) deepScan() {
	e.scanMu.Lock()
	defer e.scanMu.Unlock()
	cfg := *e.cfg
	e.mu.Lock()
	client := e.client
	e.mu.Unlock()
	rarities := activeRarities(&cfg)
	rarities = e.raritiesWithTargets(client, rarities, &cfg)
	if len(rarities) == 0 {
		e.logf("深度收购：没有启用任何采购分类，跳过")
		return
	}
	e.refreshOwned(client)
	for _, r := range rarities {
		select {
		case <-e.stopped():
			return
		default:
		}
		listings, err := fetchCategoryAsc(client, r, e.categoryMaxLimit(&cfg, r))
		if err != nil {
			e.logf("深度收购 %s 分类抓取失败: %v", r, err)
			continue
		}
		bought := 0
		for _, l := range listings {
			limit := limitFor(&cfg, l)
			if limit <= 0 || l.Remain <= 0 || l.Price > limit {
				// 定向价与类型限价并存时同一页内可买/不可买混排，不能整段 break
				continue
			}
			e.buyOne(client, l)
			bought++
		}
		e.logf("深度收购 %s：在售 %d 条，命中并尝试 %d 条", r, len(listings), bought)
	}
	e.logf("深度收购完成")
}

// fetchBuyables 按配置的扫描方式抓取候选挂牌。
// fast：只请求市场默认页（最新发布 24 条，每轮 1 个请求），适合短间隔快扫；
// thorough：按启用分类价格升序翻页，单轮请求数 = 启用分类数（一般每类只请求第一页）。
func (e *Engine) fetchBuyables(client *site.Client, cfg *config.Config) ([]market.Listing, error) {
	if cfg.EffectiveScanMode() == "fast" {
		return client.FetchMarketDefaultPage()
	}
	return e.fetchBuyablesThorough(client, cfg)
}

// fetchBuyablesThorough 按启用分类以价格升序抓取限价范围内的在售挂牌并按 listing_id 去重合并。
// 未启用任何分类时返回空且不发请求；任一分类会话失效则返回该错误由上层重登。
func (e *Engine) fetchBuyablesThorough(client *site.Client, cfg *config.Config) ([]market.Listing, error) {
	rarities := activeRarities(cfg)
	rarities = e.raritiesWithTargets(client, rarities, cfg)
	if len(rarities) == 0 {
		return nil, nil
	}
	seen := map[int]bool{}
	var all []market.Listing
	for _, r := range rarities {
		listings, err := fetchCategoryAsc(client, r, e.categoryMaxLimit(cfg, r))
		if err != nil {
			if isSessionLost(err) {
				return nil, err
			}
			e.logf("[扫描] %s 分类抓取失败: %v", r, err)
			continue
		}
		for _, l := range listings {
			if !seen[l.ListingID] {
				seen[l.ListingID] = true
				all = append(all, l)
			}
		}
	}
	return all, nil
}

// fetchCategoryAsc 抓取单个分类价格升序的在售列表。maxLimit 为该分类内所有
// 可适用限价（类型限价、SSR 单卡价、定向价）的最大值：升序下页内只要出现
// 超过 maxLimit 的挂牌，后续页只可能更贵，立即停止翻页。
// maxLimit<=0 表示该分类没有任何限价规则，不发起请求。
func fetchCategoryAsc(client *site.Client, r market.Rarity, maxLimit int) ([]market.Listing, error) {
	if maxLimit <= 0 {
		return nil, nil
	}
	return client.FetchMarketPaged(
		site.MarketQuery{Rarity: string(r), Sort: "price_asc"},
		func(page []market.Listing) bool {
			for _, l := range page {
				if l.Price > maxLimit {
					return true
				}
			}
			return false
		},
	)
}

// categoryMaxLimit 分类内所有可适用限价的最大值：类型限价、SSR 单卡价，
// 以及按称号目录缓存归入该分类的定向价格。价格升序扫描用它做翻页止损线。
func (e *Engine) categoryMaxLimit(cfg *config.Config, r market.Rarity) int {
	maxLimit := 0
	switch r {
	case market.N:
		maxLimit = cfg.Rules.N
	case market.R:
		maxLimit = cfg.Rules.R
	case market.SR:
		maxLimit = cfg.Rules.SR
	case market.UR:
		maxLimit = cfg.Rules.UR
	case market.SSR:
		for _, p := range cfg.SSRPrices {
			if p > maxLimit {
				maxLimit = p
			}
		}
	}
	e.mu.Lock()
	tr := e.titleRarity
	e.mu.Unlock()
	for name, t := range cfg.Targets {
		if tr != nil && tr[name] == r && t.Price > maxLimit {
			maxLimit = t.Price
		}
	}
	return maxLimit
}

// activeRarities 返回采购配置中启用的稀有度分类（对应限价 > 0）。
func activeRarities(cfg *config.Config) []market.Rarity {
	var out []market.Rarity
	if cfg.Rules.N > 0 {
		out = append(out, market.N)
	}
	if cfg.Rules.R > 0 {
		out = append(out, market.R)
	}
	if cfg.Rules.SR > 0 {
		out = append(out, market.SR)
	}
	if cfg.Rules.UR > 0 {
		out = append(out, market.UR)
	}
	if len(cfg.SSRPrices) > 0 {
		out = append(out, market.SSR)
	}
	return out
}

// rarityRank 稀有度展示顺序，用于定向分类追加时保持确定序（map 遍历是随机的）。
var rarityRank = map[market.Rarity]int{market.N: 0, market.R: 1, market.SR: 2, market.SSR: 3, market.UR: 4}

// raritiesWithTargets 在 activeRarities 基础上，补充定向规则涉及的稀有度分类：
// 即使该类型的限价为 0（类型收购关闭），只要存在定向单卡规则也会扫描该分类，
// 保证"只收某张卡"时仍能覆盖。称号目录缓存在 e.titleRarity，全部定向名命中
// 缓存时不再请求目录页；出现未命中（新增定向名/站点上新称号）才重新拉取。
func (e *Engine) raritiesWithTargets(client *site.Client, base []market.Rarity, cfg *config.Config) []market.Rarity {
	if len(cfg.Targets) == 0 {
		return base
	}
	e.mu.Lock()
	cached := e.titleRarity
	e.mu.Unlock()
	needFetch := cached == nil
	for name := range cfg.Targets {
		if _, ok := cached[name]; !ok {
			needFetch = true
			break
		}
	}
	if needFetch {
		catalog, err := client.FetchTitleCatalog()
		if err != nil {
			e.logf("补充定向分类失败（读称号目录）: %v", err)
			// 目录拉不到时退回缓存（若有）：宁可少补分类也别每轮都打目录页。
			if cached == nil {
				return base
			}
		} else {
			m := make(map[string]market.Rarity, len(catalog))
			for _, t := range catalog {
				m[t.Name] = t.Rarity
			}
			e.mu.Lock()
			e.titleRarity = m
			e.mu.Unlock()
			cached = m
		}
	}
	seen := make(map[market.Rarity]bool, len(base))
	for _, r := range base {
		seen[r] = true
	}
	var extra []market.Rarity
	for name := range cfg.Targets {
		r, ok := cached[name]
		if !ok || seen[r] {
			continue
		}
		seen[r] = true
		extra = append(extra, r)
	}
	sort.Slice(extra, func(i, j int) bool { return rarityRank[extra[i]] < rarityRank[extra[j]] })
	return append(base, extra...)
}

func (e *Engine) stopped() <-chan struct{} {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.stopCh
}

// BulkItem 批量操作单条结果。
type BulkItem struct {
	Name    string `json:"name"`
	Price   int    `json:"price"`
	OK      bool   `json:"ok"`
	Message string `json:"message"`
}

// BulkResult 批量操作汇总。
type BulkResult struct {
	Success int        `json:"success"`
	Failed  int        `json:"failed"`
	Items   []BulkItem `json:"items"`
}

func (r *BulkResult) add(item BulkItem) {
	r.Items = append(r.Items, item)
	if item.OK {
		r.Success++
	} else {
		r.Failed++
	}
}

// marketOpWait 批量上架/下架中每个操作之间的间隔。
var marketOpWait = func() time.Duration { return 600 * time.Millisecond }

// BulkPublish 把指定稀有度分类的可出售称号按统一单价批量上架（数量=可出售数）。
// 与常规扫描互斥执行；同步阻塞直至完成。
func (e *Engine) BulkPublish(rarities []market.Rarity, unitPrice, durationHours int) BulkResult {
	var res BulkResult
	e.scanMu.Lock()
	defer e.scanMu.Unlock()
	cfg := *e.cfg
	e.mu.Lock()
	client := e.client
	e.mu.Unlock()
	if client == nil {
		if err := e.ensureClient(&cfg); err != nil {
			res.add(BulkItem{Message: "主号会话不可用: " + err.Error()})
			return res
		}
		e.mu.Lock()
		client = e.client
		e.mu.Unlock()
	}
	if unitPrice <= 0 {
		res.add(BulkItem{Message: "单价必须大于 0"})
		return res
	}
	if durationHours <= 0 {
		durationHours = 24
	}
	want := map[market.Rarity]bool{}
	for _, r := range rarities {
		want[r] = true
	}
	if len(want) == 0 {
		res.add(BulkItem{Message: "未选择任何稀有度分类"})
		return res
	}
	publishable, err := client.FetchPublishableTitles()
	if err != nil {
		res.add(BulkItem{Message: "读取可出售称号失败: " + err.Error()})
		return res
	}
	catalog, err := client.FetchTitleCatalog()
	if err != nil {
		res.add(BulkItem{Message: "读取称号目录失败: " + err.Error()})
		return res
	}
	candidates := publishCandidates(publishable, catalog, want)
	for _, p := range candidates {
		select {
		case <-e.stopped():
			res.add(BulkItem{Name: p.Name, Message: "批量上架被停止"})
			return res
		default:
		}
		pr := client.PublishTitle(p.TitleID, p.Sellable, unitPrice, durationHours)
		e.logf("上架 %s ×%d @%d → ok=%v %s", p.Name, p.Sellable, unitPrice, pr.OK, pr.Message)
		res.add(BulkItem{Name: p.Name, Price: unitPrice, OK: pr.OK, Message: pr.Message})
		e.waitMarketOp()
	}
	return res
}

// publishCandidates 从可出售称号中筛出指定分类、可出售数 > 0 的候选。
func publishCandidates(publishable []site.PublishableTitle, catalog []market.Title, want map[market.Rarity]bool) []site.PublishableTitle {
	rarityOf := make(map[string]market.Rarity, len(catalog))
	for _, t := range catalog {
		rarityOf[t.Name] = t.Rarity
	}
	var out []site.PublishableTitle
	for _, p := range publishable {
		r, ok := rarityOf[p.Name]
		if !ok || !want[r] || p.Sellable <= 0 {
			continue
		}
		out = append(out, p)
	}
	return out
}

// BulkCancel 把当前账号在售的挂牌全部撤回（下架）。与常规扫描互斥执行。
func (e *Engine) BulkCancel() BulkResult {
	var res BulkResult
	e.scanMu.Lock()
	defer e.scanMu.Unlock()
	cfg := *e.cfg
	e.mu.Lock()
	client := e.client
	e.mu.Unlock()
	if client == nil {
		if err := e.ensureClient(&cfg); err != nil {
			res.add(BulkItem{Message: "主号会话不可用: " + err.Error()})
			return res
		}
		e.mu.Lock()
		client = e.client
		e.mu.Unlock()
	}
	ids, err := client.MyListingIDs()
	if err != nil {
		res.add(BulkItem{Message: "读取我的挂牌失败: " + err.Error()})
		return res
	}
	for _, id := range ids {
		select {
		case <-e.stopped():
			res.add(BulkItem{Message: "批量下架被停止"})
			return res
		default:
		}
		cr := client.CancelListing(id)
		e.logf("下架 listing#%d → ok=%v %s", id, cr.OK, cr.Message)
		res.add(BulkItem{Name: fmt.Sprintf("listing#%d", id), OK: cr.OK, Message: cr.Message})
		e.waitMarketOp()
	}
	return res
}

// waitMarketOp 批量操作间的小间隔，可被停止中断。
func (e *Engine) waitMarketOp() {
	timer := time.NewTimer(marketOpWait())
	defer timer.Stop()
	select {
	case <-e.stopCh:
	case <-timer.C:
	}
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
	if t, ok := cfg.Targets[l.Name]; ok && t.Price > 0 {
		return t.Price
	}
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

// maxOwnedFor 定向规则允许的背包最大持有数；未配置或 Max<=0 返回 0（不限数量）。
func maxOwnedFor(cfg *config.Config, name string) int {
	if t, ok := cfg.Targets[name]; ok {
		return t.Max
	}
	return 0
}

// refreshOwned 刷新背包持有数缓存（仅当存在带数量上限的定向规则时抓取）。
// 抓取失败只记日志，不阻断扫描；缓存保持上一轮的值。
func (e *Engine) refreshOwned(client *site.Client) {
	cfg := *e.cfg
	need := false
	for _, t := range cfg.Targets {
		if t.Max > 0 {
			need = true
			break
		}
	}
	if !need {
		return
	}
	owned, err := client.FetchOwnedTitles()
	if err != nil {
		e.logf("刷新背包持有数失败: %v", err)
		return
	}
	e.mu.Lock()
	e.owned = owned
	e.mu.Unlock()
}

// ownedCount 返回缓存中的背包持有数（未缓存时 0）。
func (e *Engine) ownedCount(name string) int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.owned[name]
}

// addOwned 购买确认成交后增加缓存持有数，避免同轮多个挂牌超收。
// owned 在 New() 中已初始化为空 map；此处再兜底一次，防止历史数据/异常路径下为 nil。
func (e *Engine) addOwned(name string, qty int) {
	if qty <= 0 {
		return
	}
	e.mu.Lock()
	if e.owned == nil {
		e.owned = make(map[string]int)
	}
	e.owned[name] += qty
	e.mu.Unlock()
}

// buyQtyLimited 计算实际可购数量。maxOwned<=0 表示不限数量（沿用现有行为）；
// 否则按背包最大持有数扣减：已持有 >= maxOwned 则跳过，否则最多补到 maxOwned。
func buyQtyLimited(remain, maxOwned, held int) (qty int, skip bool) {
	if maxOwned <= 0 {
		return remain, false
	}
	if held >= maxOwned {
		return 0, true
	}
	want := maxOwned - held
	if remain < want {
		want = remain
	}
	return want, false
}

// buyOne 执行一次购买并记录。受定向规则数量限制（Max）约束。
func (e *Engine) buyOne(client *site.Client, l market.Listing) {
	cfg := *e.cfg
	qty, skip := buyQtyLimited(l.Remain, maxOwnedFor(&cfg, l.Name), e.ownedCount(l.Name))
	if skip {
		return
	}
	cost := qty * l.Price

	dup := e.st.LastAttemptAt(l.ListingID)
	if time.Since(dup) < 10*time.Minute {
		return // 同一 listing 十分钟内不重复尝试
	}

	e.mu.Lock()
	minBalance, dryRun := e.cfg.MinBalance, e.cfg.DryRun
	e.mu.Unlock()
	pointsBefore := 0
	if !dryRun {
		points, err := client.FetchPoints()
		if err != nil {
			e.logf("获取余额失败，跳过 %s%s：%v", l.Emoji, l.Name, err)
			return
		}
		pointsBefore = points
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
		res = client.Buy(l, qty, pointsBefore)
		e.logf("购买 %s%s ×%d @%d → ok=%v submitted=%v %s", l.Emoji, l.Name, qty, l.Price, res.OK, res.Submitted, res.Message)
	}

	actualQty, actualCost := qty, cost
	if !dryRun && res.OK {
		actualQty, actualCost = res.Qty, res.Cost
	}
	rec := store.Purchase{
		Time:      time.Now(),
		ListingID: l.ListingID,
		Name:      l.Name,
		Rarity:    string(l.Rarity),
		Price:     l.Price,
		Qty:       actualQty,
		Cost:      actualCost,
		DryRun:    dryRun,
		OK:        res.OK || dryRun,
		Submitted: dryRun || res.Submitted,
		Confirmed: dryRun || res.OK,
		Message:   res.Message,
	}
	if err := e.st.Add(rec); err != nil {
		e.logf("记录落盘失败: %v", err)
	}
	if res.OK && !dryRun {
		e.addOwned(l.Name, actualQty)
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
