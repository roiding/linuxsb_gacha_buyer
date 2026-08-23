package db

import (
	"database/sql"
	"encoding/json"
	"strconv"
)

// GetSetting 读字符串设置。
func (d *DB) GetSetting(key string) (string, error) {
	var v string
	err := d.QueryRow(`SELECT value FROM settings WHERE key = ?`, key).Scan(&v)
	if err == sql.ErrNoRows {
		return "", ErrNotFound
	}
	return v, err
}

// SetSetting 写字符串设置（upsert）。
func (d *DB) SetSetting(key, value string) error {
	_, err := d.Exec(`INSERT INTO settings(key, value) VALUES(?, ?)
		ON CONFLICT(key) DO UPDATE SET value = excluded.value`, key, value)
	return err
}

// SetJSON 存 JSON 对象。
func (d *DB) SetJSON(key string, v any) error {
	raw, err := json.Marshal(v)
	if err != nil {
		return err
	}
	return d.SetSetting(key, string(raw))
}

// GetJSON 取 JSON 对象（不存在返回 ErrNotFound）。
func (d *DB) GetJSON(key string, v any) error {
	s, err := d.GetSetting(key)
	if err != nil {
		return err
	}
	return json.Unmarshal([]byte(s), v)
}

// GetInt 读整型设置。
func (d *DB) GetInt(key string, def int) int {
	s, err := d.GetSetting(key)
	if err != nil {
		return def
	}
	n, err := strconv.Atoi(s)
	if err != nil {
		return def
	}
	return n
}

// GetBool 读布尔设置。
func (d *DB) GetBool(key string, def bool) bool {
	s, err := d.GetSetting(key)
	if err != nil {
		return def
	}
	return s == "1" || s == "true"
}

// SetInt 写整型。
func (d *DB) SetInt(key string, n int) error { return d.SetSetting(key, strconv.Itoa(n)) }

// SetBool 写布尔。
func (d *DB) SetBool(key string, b bool) error {
	s := "0"
	if b {
		s = "1"
	}
	return d.SetSetting(key, s)
}
