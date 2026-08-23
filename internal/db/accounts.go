package db

import (
	"database/sql"
	"encoding/json"
	"net/http"
)

// AccountRow accounts 表的存储模型（含明文密码与序列化 cookie）。
type AccountRow struct {
	ID       int
	Role     string
	Username string
	Password string
	Note     string
	Enabled  bool
	Status   string
	UID      int
	Message  string
	LastSeen string
	Cookies  []*http.Cookie
}

func scanAccount(row interface{ Scan(...any) error }) (*AccountRow, error) {
	a := &AccountRow{}
	var enabled int
	var rawCookies []byte
	err := row.Scan(&a.ID, &a.Role, &a.Username, &a.Password, &a.Note, &enabled,
		&a.Status, &a.UID, &a.Message, &a.LastSeen, &rawCookies)
	if err == sql.ErrNoRows {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	a.Enabled = enabled != 0
	if len(rawCookies) > 0 {
		_ = json.Unmarshal(rawCookies, &a.Cookies)
	}
	return a, nil
}

const accountCols = `id, role, username, password, note, enabled, status, uid, message, last_seen, cookies`

// UpsertAccount 新增或更新账号。
func (d *DB) UpsertAccount(a *AccountRow) error {
	raw, _ := json.Marshal(a.Cookies)
	_, err := d.Exec(`INSERT INTO accounts(role, username, password, note, enabled, status, uid, message, last_seen, cookies)
		VALUES(?,?,?,?,?,?,?,?,?,?)
		ON CONFLICT(role, username) DO UPDATE SET
			password = CASE WHEN excluded.password = '' THEN accounts.password ELSE excluded.password END,
			note = excluded.note,
			enabled = excluded.enabled,
			status = excluded.status,
			uid = excluded.uid,
			message = excluded.message,
			last_seen = excluded.last_seen,
			cookies = excluded.cookies`,
		a.Role, a.Username, a.Password, a.Note, b2i(a.Enabled), a.Status, a.UID, a.Message, a.LastSeen, raw)
	return err
}

// UpdateCredentials updates credentials and invalidates the saved session.
func (d *DB) UpdateCredentials(id int, username, password, note string, enabled bool) error {
	_, err := d.Exec(`UPDATE accounts SET username=?, password=CASE WHEN ?='' THEN password ELSE ? END,
		note=?, enabled=?, status='expired', message='凭据已变更', uid=0, last_seen='', cookies=NULL WHERE id=?`,
		username, password, password, note, b2i(enabled), id)
	return err
}

// UpdateAccountSession 只更新会话相关字段。
func (d *DB) UpdateAccountSession(role, username, status, message, lastSeen string, uid int, cookies []*http.Cookie) error {
	raw, _ := json.Marshal(cookies)
	_, err := d.Exec(`UPDATE accounts SET status=?, message=?, last_seen=?, uid=?, cookies=? WHERE role=? AND username=?`,
		status, message, lastSeen, uid, raw, role, username)
	return err
}

func (d *DB) GetAccount(role, username string) (*AccountRow, error) {
	return scanAccount(d.QueryRow(`SELECT `+accountCols+` FROM accounts WHERE role=? AND username=?`, role, username))
}

func (d *DB) GetAccountByID(id int) (*AccountRow, error) {
	return scanAccount(d.QueryRow(`SELECT `+accountCols+` FROM accounts WHERE id=?`, id))
}

func (d *DB) ListAccounts() ([]*AccountRow, error) {
	rows, err := d.Query(`SELECT ` + accountCols + ` FROM accounts ORDER BY CASE role WHEN 'main' THEN 0 ELSE 1 END, username`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*AccountRow
	for rows.Next() {
		a, err := scanAccount(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

func (d *DB) DeleteAccount(role, username string) error {
	_, err := d.Exec(`DELETE FROM accounts WHERE role=? AND username=?`, role, username)
	return err
}

func (d *DB) DeleteAccountByID(id int) error {
	_, err := d.Exec(`DELETE FROM accounts WHERE id=?`, id)
	return err
}

func b2i(b bool) int {
	if b {
		return 1
	}
	return 0
}
