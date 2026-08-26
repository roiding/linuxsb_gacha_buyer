// Package web 内置 Web 控制台。
package web

import (
	"embed"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"gacha-buyer/internal/accounts"
	"gacha-buyer/internal/buyer"
	"gacha-buyer/internal/collector"
	"gacha-buyer/internal/config"
	"gacha-buyer/internal/db"
	"gacha-buyer/internal/lottery"
	"gacha-buyer/internal/store"
)

//go:embed static
var staticFS embed.FS

// Server Web 控制台。
type Server struct {
	cfg *config.Config
	d   *db.DB
	st  *store.Store
	eng *buyer.Engine
	mgr *accounts.Manager
	col *collector.Engine
	lot *lottery.Engine
}

// New 创建 Web 服务。
func New(cfg *config.Config, d *db.DB, st *store.Store, eng *buyer.Engine, mgr *accounts.Manager, col *collector.Engine, lot *lottery.Engine) *Server {
	return &Server{cfg: cfg, d: d, st: st, eng: eng, mgr: mgr, col: col, lot: lot}
}

// Handler 组装路由。
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/", s.handleIndex)
	mux.HandleFunc("/static/", s.handleStatic)
	mux.HandleFunc("/api/status", s.handleStatus)
	mux.HandleFunc("/api/purchases", s.handlePurchases)
	mux.HandleFunc("/api/config", s.handleConfig)
	mux.HandleFunc("/api/catalog", s.handleCatalog)
	mux.HandleFunc("/api/engine/start", s.handleStart)
	mux.HandleFunc("/api/engine/stop", s.handleStop)
	mux.HandleFunc("/api/engine/scan", s.handleScanOnce)
	mux.HandleFunc("/api/accounts", s.handleAccounts)
	mux.HandleFunc("/api/accounts/recover", s.handleAccountRecover)
	mux.HandleFunc("/api/accounts/logout", s.handleAccountLogout)
	mux.HandleFunc("/api/accounts/patrol", s.handlePatrolOnce)
	mux.HandleFunc("/api/transfers", s.handleTransfers)
	mux.HandleFunc("/api/collector/run", s.handleCollectorRun)
	mux.HandleFunc("/api/lottery", s.handleLottery)
	mux.HandleFunc("/api/lottery/run", s.handleLotteryRun)
	mux.HandleFunc("/api/market/publish", s.handleMarketPublish)
	mux.HandleFunc("/api/market/cancel", s.handleMarketCancel)
	return mux
}

// ListenAndServe 启动 HTTP 服务（阻塞）。
func (s *Server) ListenAndServe() error {
	srv := &http.Server{
		Addr:              s.cfg.Listen,
		Handler:           s.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
	}
	return srv.ListenAndServe()
}

func (s *Server) handleStatic(w http.ResponseWriter, r *http.Request) {
	name := strings.TrimPrefix(r.URL.Path, "/static/")
	if strings.Contains(name, "..") || name == "" {
		http.NotFound(w, r)
		return
	}
	data, err := staticFS.ReadFile("static/" + name)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	switch {
	case strings.HasSuffix(name, ".css"):
		w.Header().Set("Content-Type", "text/css; charset=utf-8")
	case strings.HasSuffix(name, ".js"):
		w.Header().Set("Content-Type", "application/javascript")
	default:
		w.Header().Set("Content-Type", "application/octet-stream")
	}
	w.Write(data)
}

func (s *Server) handleIndex(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	fmt.Fprint(w, indexHTML)
}

func (s *Server) handleStatus(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, s.eng.Snapshot())
}

func (s *Server) handlePurchases(w http.ResponseWriter, r *http.Request) {
	recs := s.st.All()
	limit := 200
	if l := r.URL.Query().Get("limit"); l != "" {
		if n, err := strconv.Atoi(l); err == nil && n > 0 && n <= 1000 {
			limit = n
		}
	}
	if len(recs) > limit {
		recs = recs[:limit]
	}
	writeJSON(w, map[string]any{
		"records":     recs,
		"total_spent": s.st.TotalSpent(),
		"ok_count":    s.st.CountOK(),
	})
}

// handleConfig GET 返回配置（隐藏密码），POST 更新并落盘。
func (s *Server) handleConfig(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		writeJSON(w, map[string]any{
			"site":        s.cfg.Site,
			"rules":       s.cfg.Rules,
			"ssr_prices":  s.cfg.SSRPrices,
			"targets":     s.cfg.Targets,
			"min_balance": s.cfg.MinBalance,
			"dry_run":     s.cfg.DryRun,
			"scan_sec":    s.cfg.ScanSec,
			"listen":      s.cfg.Listen,
		})
	case http.MethodPost:
		var in struct {
			Rules      *config.PriceRules            `json:"rules"`
			SSRPrices  *map[string]int               `json:"ssr_prices"`
			Targets    *map[string]config.TargetRule `json:"targets"`
			MinBalance *int                          `json:"min_balance"`
			DryRun     *bool                         `json:"dry_run"`
			ScanSec    *int                          `json:"scan_sec"`
		}

		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			httpError(w, 400, "请求体不是合法 JSON")
			return
		}
		if in.Rules != nil {
			s.cfg.Rules = *in.Rules
		}
		if in.SSRPrices != nil {
			s.cfg.SSRPrices = *in.SSRPrices
		}
		if in.Targets != nil {
			s.cfg.Targets = *in.Targets
		}
		if in.MinBalance != nil && *in.MinBalance >= 0 {
			s.cfg.MinBalance = *in.MinBalance
		}

		if in.DryRun != nil {
			s.cfg.DryRun = *in.DryRun
		}
		if in.ScanSec != nil {
			s.cfg.ScanSec = *in.ScanSec
		}
		s.cfg.Normalize()
		if err := config.Save(s.d, s.cfg); err != nil {
			httpError(w, 500, "保存配置失败: "+err.Error())
			return
		}
		// 收购参数变更立即由引擎下次扫描读取；同时按启用的稀有度分类触发一次深度收购，
		// 覆盖"最新发布 100 条"之外的便宜挂牌（后台执行，不阻塞保存请求）。
		if err := s.eng.DeepScanNow(); err != nil {
			writeJSON(w, map[string]any{"ok": true, "message": "配置已保存，但分类深度收购未触发: " + err.Error()})
			return
		}
		writeJSON(w, map[string]any{"ok": true, "message": "配置已保存，分类深度收购已开始"})
	default:
		httpError(w, 405, "方法不允许")
	}
}

func (s *Server) handleCatalog(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		httpError(w, http.StatusMethodNotAllowed, "方法不允许")
		return
	}
	client, _, err := s.mgr.Main()
	if err != nil {
		httpError(w, http.StatusBadRequest, err.Error())
		return
	}
	titles, err := client.FetchTitleCatalog()
	if err != nil {
		httpError(w, http.StatusBadGateway, err.Error())
		return
	}
	writeJSON(w, map[string]any{"titles": titles, "targets": s.cfg.Targets})
}

func (s *Server) handleStart(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		httpError(w, 405, "方法不允许")
		return
	}
	if s.cfg.Username == "" || s.cfg.Password == "" {
		httpError(w, 400, "请先在账号管理中填写主号账号密码")
		return
	}
	if err := s.eng.Start(); err != nil {
		httpError(w, 500, err.Error())
		return
	}
	writeJSON(w, map[string]any{"ok": true})
}

func (s *Server) handleStop(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		httpError(w, 405, "方法不允许")
		return
	}
	s.eng.Stop()
	writeJSON(w, map[string]any{"ok": true})
}

func (s *Server) handleScanOnce(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		httpError(w, 405, "方法不允许")
		return
	}
	if err := s.eng.ScanNow(); err != nil {
		writeJSON(w, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	writeJSON(w, map[string]any{"ok": true})
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	json.NewEncoder(w).Encode(v)
}

func httpError(w http.ResponseWriter, code int, msg string) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(map[string]string{"error": msg})
}

// maskUser 用户名掩码：se7en@example.com → se***@example.com
func maskUser(u string) string {
	if u == "" {
		return ""
	}
	at := strings.Index(u, "@")
	name, domain := u, ""
	if at >= 0 {
		name, domain = u[:at], u[at:]
	}
	r := []rune(name)
	keep := 2
	if len(r) <= keep {
		return name + "***" + domain
	}
	return string(r[:keep]) + "***" + domain
}
