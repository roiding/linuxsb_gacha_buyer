// Package db SQLite 存储：配置、账号会话、购买与归集记录全部入库。
package db

import (
	"database/sql"
	"errors"
	"fmt"

	_ "modernc.org/sqlite"
)

// DB 封装 *sql.DB。
type DB struct {
	*sql.DB
}

// Open 打开（必要时创建）数据库并执行迁移。
func Open(path string) (*DB, error) {
	d, err := sql.Open("sqlite", path+"?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)")
	if err != nil {
		return nil, err
	}
	d.SetMaxOpenConns(1) // SQLite 单写，避免锁竞争
	if err := d.Ping(); err != nil {
		return nil, fmt.Errorf("sqlite 打开失败: %w", err)
	}
	if err := migrate(d); err != nil {
		return nil, err
	}
	return &DB{d}, nil
}

func migrate(d *sql.DB) error {
	stmts := []string{
		`CREATE TABLE IF NOT EXISTS settings (
			key   TEXT PRIMARY KEY,
			value TEXT NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS accounts (
			id        INTEGER PRIMARY KEY AUTOINCREMENT,
			role      TEXT NOT NULL,             -- main | sub
			username  TEXT NOT NULL,
			password  TEXT NOT NULL DEFAULT '',
			note      TEXT NOT NULL DEFAULT '',
			enabled   INTEGER NOT NULL DEFAULT 1,
			status    TEXT NOT NULL DEFAULT 'expired', -- ok|expired|error
			uid       INTEGER NOT NULL DEFAULT 0,
			message   TEXT NOT NULL DEFAULT '',
			last_seen TEXT NOT NULL DEFAULT '',
			cookies   BLOB,                      -- 序列化的会话 cookie
			UNIQUE(role, username)
		)`,
		`CREATE TABLE IF NOT EXISTS purchases (
				id         INTEGER PRIMARY KEY AUTOINCREMENT,
				time       TEXT NOT NULL,
				listing_id INTEGER NOT NULL,
				name       TEXT NOT NULL,
				rarity     TEXT NOT NULL,
				price      INTEGER NOT NULL,
				qty        INTEGER NOT NULL,
				cost       INTEGER NOT NULL,
				dry_run    INTEGER NOT NULL DEFAULT 0,
				ok         INTEGER NOT NULL DEFAULT 0,
				submitted  INTEGER NOT NULL DEFAULT 0,
				confirmed  INTEGER NOT NULL DEFAULT 0,
				message    TEXT NOT NULL DEFAULT ''
			)`,

		`CREATE TABLE IF NOT EXISTS transfers (
				id            INTEGER PRIMARY KEY AUTOINCREMENT,
				time          TEXT NOT NULL,
				sub           TEXT NOT NULL,
				account_id    INTEGER NOT NULL DEFAULT 0,
				check_in      INTEGER NOT NULL DEFAULT 0,
				balance       INTEGER NOT NULL DEFAULT 0,
				balance_before INTEGER NOT NULL DEFAULT 0,
				balance_after  INTEGER NOT NULL DEFAULT 0,
				tip_amount    INTEGER NOT NULL DEFAULT 0,
				topic_id      INTEGER NOT NULL DEFAULT 0,
				dry_run       INTEGER NOT NULL DEFAULT 0,
				ok            INTEGER NOT NULL DEFAULT 0,
				submitted     INTEGER NOT NULL DEFAULT 0,
				confirmed     INTEGER NOT NULL DEFAULT 0,
					pending        INTEGER NOT NULL DEFAULT 0,
					retryable      INTEGER NOT NULL DEFAULT 1,
					http_status    INTEGER NOT NULL DEFAULT 0,

				attempt       INTEGER NOT NULL DEFAULT 0,
				message       TEXT NOT NULL DEFAULT ''
			)`,
		`CREATE TABLE IF NOT EXISTS collector_schedule (
			day          TEXT NOT NULL,
			account      TEXT NOT NULL,
			planned_at   TEXT NOT NULL,
			started_at   TEXT NOT NULL DEFAULT '',
			completed_at TEXT NOT NULL DEFAULT '',
			status       TEXT NOT NULL DEFAULT 'planned',
			retries      INTEGER NOT NULL DEFAULT 0,
			PRIMARY KEY (day, account)
		)`,
		`CREATE TABLE IF NOT EXISTS lottery_replies (
			id          INTEGER PRIMARY KEY AUTOINCREMENT,
			time        TEXT NOT NULL,
			account_id  INTEGER NOT NULL DEFAULT 0,
			sub         TEXT NOT NULL,
			topic_id    INTEGER NOT NULL,
			content     TEXT NOT NULL,
			captcha     INTEGER NOT NULL DEFAULT 0,
			dry_run     INTEGER NOT NULL DEFAULT 0,
			submitted   INTEGER NOT NULL DEFAULT 0,
			confirmed   INTEGER NOT NULL DEFAULT 0,
			reply_id    INTEGER NOT NULL DEFAULT 0,
			message     TEXT NOT NULL DEFAULT ''
		)`,
		`CREATE TABLE IF NOT EXISTS gacha_draws (
			id          INTEGER PRIMARY KEY AUTOINCREMENT,
			day         TEXT NOT NULL,
			time        TEXT NOT NULL,
			account_id  INTEGER NOT NULL DEFAULT 0,
			sub         TEXT NOT NULL DEFAULT '',
			drawn       TEXT NOT NULL DEFAULT '',
			ok          INTEGER NOT NULL DEFAULT 0,
			gifted      INTEGER NOT NULL DEFAULT 0,
			gift_target TEXT NOT NULL DEFAULT '',
			message     TEXT NOT NULL DEFAULT '',
			UNIQUE(day, account_id)
		)`,
	}
	for _, s := range stmts {
		if _, err := d.Exec(s); err != nil {
			return fmt.Errorf("迁移失败: %w", err)
		}
	}
	// collector_schedule 旧版按 day 一行存单个计划点；新版按 day+account 一行。
	// 计划属于临时数据，检测到旧结构直接重建，次日/重启会自动重新生成。
	if ok, err := hasColumn(d, "collector_schedule", "account"); err != nil {
		return err
	} else if !ok {
		if _, err := d.Exec(`DROP TABLE collector_schedule`); err != nil {
			return fmt.Errorf("迁移 collector_schedule 失败: %w", err)
		}
		if _, err := d.Exec(`CREATE TABLE collector_schedule (
			day          TEXT NOT NULL,
			account      TEXT NOT NULL,
			planned_at   TEXT NOT NULL,
			started_at   TEXT NOT NULL DEFAULT '',
			completed_at TEXT NOT NULL DEFAULT '',
			status       TEXT NOT NULL DEFAULT 'planned',
			retries      INTEGER NOT NULL DEFAULT 0,
			PRIMARY KEY (day, account)
		)`); err != nil {
			return fmt.Errorf("重建 collector_schedule 失败: %w", err)
		}
	}
	if ok, err := hasColumn(d, "collector_schedule", "retries"); err != nil {
		return err
	} else if !ok {
		if _, err := d.Exec(`ALTER TABLE collector_schedule ADD COLUMN retries INTEGER NOT NULL DEFAULT 0`); err != nil {
			return fmt.Errorf("迁移 collector_schedule.retries 失败: %w", err)
		}
	}
	if ok, err := hasColumn(d, "purchases", "submitted"); err != nil {
		return err
	} else if !ok {
		if _, err := d.Exec(`ALTER TABLE purchases ADD COLUMN submitted INTEGER NOT NULL DEFAULT 0`); err != nil {
			return fmt.Errorf("迁移 purchases.submitted 失败: %w", err)
		}
	}
	if ok, err := hasColumn(d, "purchases", "confirmed"); err != nil {
		return err
	} else if !ok {
		if _, err := d.Exec(`ALTER TABLE purchases ADD COLUMN confirmed INTEGER NOT NULL DEFAULT 0`); err != nil {
			return fmt.Errorf("迁移 purchases.confirmed 失败: %w", err)
		}
	}
	if ok, err := hasColumn(d, "transfers", "account_id"); err != nil {
		return err
	} else if !ok {
		if _, err := d.Exec(`ALTER TABLE transfers ADD COLUMN account_id INTEGER NOT NULL DEFAULT 0`); err != nil {
			return fmt.Errorf("迁移 transfers.account_id 失败: %w", err)
		}
	}
	for _, col := range []struct {
		name string
		ddl  string
	}{
		{"balance_before", `ALTER TABLE transfers ADD COLUMN balance_before INTEGER NOT NULL DEFAULT 0`},
		{"balance_after", `ALTER TABLE transfers ADD COLUMN balance_after INTEGER NOT NULL DEFAULT 0`},
		{"submitted", `ALTER TABLE transfers ADD COLUMN submitted INTEGER NOT NULL DEFAULT 0`},
		{"confirmed", `ALTER TABLE transfers ADD COLUMN confirmed INTEGER NOT NULL DEFAULT 0`},
		{"pending", `ALTER TABLE transfers ADD COLUMN pending INTEGER NOT NULL DEFAULT 0`},
		{"retryable", `ALTER TABLE transfers ADD COLUMN retryable INTEGER NOT NULL DEFAULT 1`},
		{"http_status", `ALTER TABLE transfers ADD COLUMN http_status INTEGER NOT NULL DEFAULT 0`},

		{"attempt", `ALTER TABLE transfers ADD COLUMN attempt INTEGER NOT NULL DEFAULT 0`},
	} {
		ok, err := hasColumn(d, "transfers", col.name)
		if err != nil {
			return err
		}
		if !ok {
			if _, err := d.Exec(col.ddl); err != nil {
				return fmt.Errorf("迁移 transfers.%s 失败: %w", col.name, err)
			}
		}
	}
	return nil

}

func hasColumn(d *sql.DB, table, column string) (bool, error) {
	rows, err := d.Query(`PRAGMA table_info(` + table + `)`)
	if err != nil {
		return false, err
	}
	defer rows.Close()
	for rows.Next() {
		var cid, notNull, pk int
		var name, typ string
		var def any
		if err := rows.Scan(&cid, &name, &typ, &notNull, &def, &pk); err != nil {
			return false, err
		}
		if name == column {
			return true, nil
		}
	}
	return false, rows.Err()
}

// ErrNotFound 记录不存在。
var ErrNotFound = errors.New("not found")
