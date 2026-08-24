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
			id         INTEGER PRIMARY KEY AUTOINCREMENT,
			time       TEXT NOT NULL,
			sub        TEXT NOT NULL,
			check_in   INTEGER NOT NULL DEFAULT 0,
			balance    INTEGER NOT NULL DEFAULT 0,
			tip_amount INTEGER NOT NULL DEFAULT 0,
			topic_id   INTEGER NOT NULL DEFAULT 0,
			dry_run    INTEGER NOT NULL DEFAULT 0,
			ok         INTEGER NOT NULL DEFAULT 0,
			message    TEXT NOT NULL DEFAULT ''
		)`,
	}
	for _, s := range stmts {
		if _, err := d.Exec(s); err != nil {
			return fmt.Errorf("迁移失败: %w", err)
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
