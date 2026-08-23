// Package accounts 多账号管理：主号+小号的会话持久化、健康巡检与状态维护。
// 会话与状态全部持久化到 SQLite accounts 表。
package accounts

import (
	"errors"
	"fmt"
	"sync"
	"time"

	"gacha-buyer/internal/config"
	"gacha-buyer/internal/db"
	"gacha-buyer/internal/site"
)

// Status 账号状态。
type Status string

const (
	StatusOK      Status = "ok"      // 会话正常
	StatusExpired Status = "expired" // 掉线（可自动重登）
	StatusError   Status = "error"   // 异常（登录失败等，需人工恢复）
)

// Acct 单个账号的运行时状态。
type Acct struct {
	ID       int
	Role     string
	Username string
	Status   Status
	UID      int
	Message  string
	LastSeen string

	client *site.Client
}

// Manager 管理所有账号的客户端与会话。
type Manager struct {
	mu      sync.Mutex
	cfg     *config.Config
	d       *db.DB
	accts   map[string]*Acct
	logf    func(string, ...any)
	stopCh  chan struct{}
	stopped chan struct{}
}

// New 创建 Manager；从 SQLite 加载会话 cookie。
func New(cfg *config.Config, d *db.DB, logf func(string, ...any)) (*Manager, error) {
	if logf == nil {
		logf = func(string, ...any) {}
	}
	m := &Manager{
		cfg:     cfg,
		d:       d,
		accts:   map[string]*Acct{},
		logf:    logf,
		stopCh:  make(chan struct{}),
		stopped: make(chan struct{}),
	}
	m.syncFromDB()
	return m, nil
}

// syncFromDB 把数据库里的账号（含持久化 cookie）加载进内存，并与配置同步增删。
func (m *Manager) syncFromDB() {
	m.mu.Lock()
	defer m.mu.Unlock()
	rows, err := m.d.ListAccounts()
	if err == nil {
		for _, r := range rows {
			key := r.Role + "|" + r.Username
			a := m.accts[key]
			if a == nil {
				a = &Acct{Role: r.Role, Username: r.Username}
				m.accts[key] = a
			}
			a.ID = r.ID
			a.Status = Status(r.Status)
			a.UID = r.UID
			a.Message = r.Message
			a.LastSeen = r.LastSeen
			if a.client != nil && len(r.Cookies) > 0 {
				a.client.ImportCookies(r.Cookies)
			}
			m.pendingCookies(key, r.Cookies)
		}
	}
}

// pendingCookies 预留（cookie 已直接从 DB 行注入 client）。
func (m *Manager) pendingCookies(key string, cookies any) {}

// syncFromConfig 把配置中的账密同步进 accounts 表（新增/删除/改密）。
func (m *Manager) SyncFromConfig() {
	m.mu.Lock()
	defer m.mu.Unlock()
	want := map[string]bool{}
	upsert := func(role, username, password string) {
		if username == "" {
			return
		}
		want[role+"|"+username] = true
		existing, err := m.d.GetAccount(role, username)
		if err == db.ErrNotFound {
			_ = m.d.UpsertAccount(&db.AccountRow{
				Role: role, Username: username, Password: password,
				Enabled: true, Status: string(StatusExpired),
			})
			return
		}
		if err == nil && password != "" && password != existing.Password {
			_ = m.d.UpdateCredentials(existing.ID, existing.Username, password, existing.Note, existing.Enabled)
			if a := m.accts[role+"|"+username]; a != nil {
				a.client = nil
				a.Status = StatusExpired
				a.Message = "凭据已变更"
			}
		}
	}
	upsert("main", m.cfg.Username, m.cfg.Password)
	for _, s := range m.cfg.Subs {
		upsert("sub", s.Username, s.Password)
	}
	rows, _ := m.d.ListAccounts()
	for _, r := range rows {
		if !want[r.Role+"|"+r.Username] {
			_ = m.d.DeleteAccount(r.Role, r.Username)
			delete(m.accts, r.Role+"|"+r.Username)
		}
	}
	for _, s := range m.cfg.Subs {
		if a, err := m.d.GetAccount("sub", s.Username); err == nil {
			a.Enabled = s.Enabled
			a.Note = s.Note
			_ = m.d.UpsertAccount(a)
		}
	}
}

// Resync 配置变化后重新同步（前端调用）。
func (m *Manager) Resync() { m.SyncFromConfig() }

// clientFor 返回某账号可用客户端：优先复用 SQLite 会话，失效自动重登。
func (m *Manager) clientFor(role, username, password string) (*site.Client, *Acct, error) {
	m.mu.Lock()
	key := role + "|" + username
	a := m.accts[key]
	if a == nil {
		a = &Acct{Role: role, Username: username, Status: StatusExpired}
		m.accts[key] = a
	}
	m.mu.Unlock()

	row, err := m.d.GetAccount(role, username)
	if err != nil && err != db.ErrNotFound {
		return nil, a, err
	}
	if row == nil {
		row = &db.AccountRow{Role: role, Username: username, Password: password, Enabled: true, Status: string(StatusExpired)}
		if err := m.d.UpsertAccount(row); err != nil {
			return nil, a, err
		}
		row, err = m.d.GetAccount(role, username)
		if err != nil {
			return nil, a, err
		}
	}
	a.ID = row.ID
	if password != "" && row.Password != password {
		row.Password = password // 配置热更新了密码
	}

	m.mu.Lock()
	needNew := a.client == nil
	m.mu.Unlock()
	if needNew {
		c, err := site.NewClientFor(m.cfg, row.Username, row.Password, m.logf)
		if err != nil {
			return nil, a, err
		}
		c.ImportCookies(row.Cookies)
		m.mu.Lock()
		a.client = c
		m.mu.Unlock()
	}
	m.mu.Lock()
	c := a.client
	m.mu.Unlock()
	if c == nil {
		return nil, a, errors.New("客户端不可用")
	}

	if !c.IsLoggedIn() {
		m.logf("[%s] %s 会话失效，重新登录…", role, username)
		if err := c.Login(); err != nil {
			_ = m.d.UpdateAccountSession(role, username, string(StatusError), "登录失败: "+err.Error(), "", row.UID, row.Cookies)
			m.mu.Lock()
			a.Status = StatusError
			a.Message = "登录失败: " + err.Error()
			m.mu.Unlock()
			return nil, a, fmt.Errorf("登录 %s 失败: %w", username, err)
		}
		now := time.Now().Format("2006-01-02 15:04:05")
		uid, _ := c.GetMyUID()
		_ = m.d.UpdateAccountSession(role, username, string(StatusOK), "", now, uid, c.ExportCookies())
		m.mu.Lock()
		a.Status = StatusOK
		a.Message = ""
		a.UID = uid
		a.LastSeen = now
		m.mu.Unlock()
	} else if a.Status != StatusOK {
		m.mu.Lock()
		a.Status = StatusOK
		a.Message = "复用已有会话"
		m.mu.Unlock()
	}
	return c, a, nil
}

// Main 主号客户端。
func (m *Manager) Main() (*site.Client, *Acct, error) {
	if m.cfg.Username == "" || m.cfg.Password == "" {
		return nil, nil, errors.New("主号未设置，请先在账号管理中填写")
	}
	return m.clientFor("main", m.cfg.Username, m.cfg.Password)
}

// Sub 小号客户端。
func (m *Manager) Sub(s config.SubAccount) (*site.Client, *Acct, error) {
	return m.clientFor("sub", s.Username, s.Password)
}

// Probe 巡检单个账号：在线复用，掉线重登，重登失败置异常。
func (m *Manager) Probe(role, username, password string) {
	_, _, err := m.clientFor(role, username, password)
	if err != nil {
		m.logf("巡检异常: %v", err)
	}
	m.mu.Lock()
	a := m.accts[role+"|"+username]
	if a != nil && a.Status == StatusOK && a.client != nil {
		if uid, uerr := a.client.GetMyUID(); uerr == nil {
			a.UID = uid
		}
		a.LastSeen = time.Now().Format("2006-01-02 15:04:05")
		m.mu.Unlock()
		_ = m.d.UpdateAccountSession(role, username, string(a.Status), a.Message, a.LastSeen, a.UID, a.client.ExportCookies())
		return
	}
	m.mu.Unlock()
}

// SetUID 巡检外更新账号 UID。
func (m *Manager) SetUID(role, username string, uid int) {
	m.mu.Lock()
	a := m.accts[role+"|"+username]
	var c *site.Client
	if a != nil {
		c = a.client
		a.UID = uid
	}
	m.mu.Unlock()
	row, err := m.d.GetAccount(role, username)
	if err == nil {
		row.UID = uid
		if c != nil {
			row.Cookies = c.ExportCookies()
		}
		_ = m.d.UpsertAccount(row)
	}
}

// Info 给前端展示的账号条目。
type Info struct {
	ID          int    `json:"id"`
	Role        string `json:"role"`
	Username    string `json:"username"`
	Status      Status `json:"status"`
	UID         int    `json:"uid,omitempty"`
	Message     string `json:"message,omitempty"`
	LastSeen    string `json:"last_seen,omitempty"`
	Note        string `json:"note,omitempty"`
	Enabled     bool   `json:"enabled"`
	PasswordSet bool   `json:"password_set"`
}

// Snapshot 全部账号当前状态（主号在前），直接读 DB。
func (m *Manager) Snapshot() []Info {
	rows, err := m.d.ListAccounts()
	if err != nil {
		return []Info{}
	}
	out := make([]Info, 0, len(rows))
	for _, r := range rows {
		out = append(out, Info{
			ID: r.ID, Role: r.Role, Username: maskUser(r.Username), Status: Status(r.Status),
			UID: r.UID, Message: r.Message, LastSeen: r.LastSeen, Note: r.Note,
			Enabled: r.Enabled, PasswordSet: r.Password != "",
		})
	}
	return out
}

// LogoutByID lets the API operate on an opaque database account identifier.
func (m *Manager) LogoutByID(id int) error {
	row, err := m.d.GetAccountByID(id)
	if err != nil {
		return err
	}
	return m.Logout(row.Role, row.Username)
}

// RecoverByID lets the API operate on an opaque database account identifier.
func (m *Manager) RecoverByID(id int) error {
	row, err := m.d.GetAccountByID(id)
	if err != nil {
		return err
	}
	return m.Recover(row.Role, row.Username)
}

// Logout 让指定账号退出站点登录并清空本地会话。
func (m *Manager) Logout(role, username string) error {
	m.mu.Lock()
	a := m.accts[role+"|"+username]
	var c *site.Client
	if a != nil {
		c = a.client
		a.client = nil
		a.Status = StatusExpired
		a.Message = "已在站点退出登录"
		a.UID = 0
	}
	m.mu.Unlock()
	row, _ := m.d.GetAccount(role, username)
	if row != nil {
		row.Status = string(StatusExpired)
		row.Message = "已在站点退出登录"
		row.UID = 0
		row.Cookies = nil
		_ = m.d.UpsertAccount(row)
	}
	if c != nil {
		_ = c.Logout()
	}
	return nil
}

// Recover 人工恢复：清空错误状态并强制重新登录。
func (m *Manager) Recover(role, username string) error {
	row, err := m.d.GetAccount(role, username)
	if err != nil {
		return err
	}
	if row.Password == "" {
		return errors.New("找不到该账号密码，请先在账号管理中填写")
	}
	c, errC := site.NewClientFor(m.cfg, row.Username, row.Password, m.logf)
	if errC != nil {
		return errC
	}
	if err := c.Login(); err != nil {
		msg := "人工恢复失败: " + err.Error()
		_ = m.d.UpdateAccountSession(role, username, string(StatusError), msg, "", 0, nil)
		m.mu.Lock()
		if a := m.accts[role+"|"+username]; a != nil {
			a.Message = msg
		}
		m.mu.Unlock()
		return err
	}
	now := time.Now().Format("2006-01-02 15:04:05")
	uid, _ := c.GetMyUID()
	_ = m.d.UpdateAccountSession(role, username, string(StatusOK), "人工恢复成功", now, uid, c.ExportCookies())
	m.mu.Lock()
	if a := m.accts[role+"|"+username]; a != nil {
		a.client = c
		a.Status = StatusOK
		a.Message = "人工恢复成功"
		a.UID = uid
		a.LastSeen = now
	}
	m.mu.Unlock()
	return nil
}

// PatrolOnce 对外暴露的单次巡检。
func (m *Manager) PatrolOnce() { m.patrolOnce() }

// StartPatrol 每 4 小时巡检全部账号。
func (m *Manager) StartPatrol() {
	m.mu.Lock()
	if m.stopped != nil {
		select {
		case <-m.stopped:
		default:
		}
	}
	m.stopCh = make(chan struct{})
	m.stopped = make(chan struct{})
	stopCh, stopped := m.stopCh, m.stopped
	m.mu.Unlock()
	go func() {
		defer close(stopped)
		t := time.NewTicker(4 * time.Hour)
		defer t.Stop()
		m.patrolOnce()
		for {
			select {
			case <-stopCh:
				return
			case <-t.C:
				m.patrolOnce()
			}
		}
	}()
}

// StopPatrol 停止巡检。
func (m *Manager) StopPatrol() {
	select {
	case <-m.stopCh:
	default:
		close(m.stopCh)
	}
	select {
	case <-m.stopped:
	case <-time.After(5 * time.Second):
	}
}

func (m *Manager) patrolOnce() {
	m.logf("开始账号巡检…")
	if m.cfg.Username != "" && m.cfg.Password != "" {
		m.Probe("main", m.cfg.Username, m.cfg.Password)
	}
	for _, s := range m.cfg.Subs {
		if !s.Enabled || s.Username == "" {
			continue
		}
		m.Probe("sub", s.Username, s.Password)
	}
	m.logf("账号巡检完成")
}

func maskUser(u string) string {
	at := -1
	for i := 0; i < len(u); i++ {
		if u[i] == '@' {
			at = i
			break
		}
	}
	name, domain := u, ""
	if at >= 0 {
		name, domain = u[:at], u[at:]
	}
	r := []rune(name)
	if len(r) <= 2 {
		return name + "***" + domain
	}
	return string(r[:2]) + "***" + domain
}
