package config

import "gacha-buyer/internal/db"

const keyConfig = "config"

// Load 从数据库 settings 表加载配置；无记录时返回默认值并立即持久化。
func Load(d *db.DB) (Config, error) {
	cfg := Defaults()
	err := d.GetJSON(keyConfig, &cfg)
	if err != nil {
		if err == db.ErrNotFound {
			cfg.Normalize()
			return cfg, Save(d, &cfg)
		}
		return cfg, err
	}
	cfg.Normalize()
	return cfg, nil
}

// Save 配置整体写入 SQLite settings 表。
func Save(d *db.DB, c *Config) error {
	c.Normalize()
	return d.SetJSON(keyConfig, c)
}
