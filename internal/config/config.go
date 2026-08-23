// Package config 配置模型与默认值。全部持久化由 internal/db 的 settings 表承担。
package config

// PriceRules 按稀有度的单卡限价（积分），0 表示该稀有度不收购。
type PriceRules struct {
	SR  int `json:"sr"`
	R   int `json:"r"`
	N   int `json:"n"`
	SSR int `json:"ssr"`
	UR  int `json:"ur"`
}

// SubAccount 小号：负责每日签到并把积分打赏给主号帖子。
type SubAccount struct {
	Username string `json:"username"`
	Password string `json:"password"`
	Note     string `json:"note,omitempty"`
	Enabled  bool   `json:"enabled"`
}

// Collector 归集任务配置。
type Collector struct {
	TopicID int    `json:"topic_id"` // 0 = 每日随机挑主号帖子
	Keep    int    `json:"keep"`
	AtHour  int    `json:"at_hour"`
	Message string `json:"message"`
	MinTip  int    `json:"min_tip"`
}

// Config 完整运行配置（内存态，由 db 加载/保存）。
type Config struct {
	Site     string `json:"site"`
	Username string `json:"username"` // 主号
	Password string `json:"password"`

	Rules       PriceRules `json:"rules"`
	MaxSpend    int        `json:"max_spend"`
	DryRun      bool       `json:"dry_run"`
	ScanSec     int        `json:"scan_sec"`
	MaxBuyOnce  int        `json:"max_buy_once"`
	MaxListings int        `json:"max_listings"`

	Subs      []SubAccount `json:"subs"`
	Collector Collector    `json:"collector"`

	Listen string `json:"listen"`
}

// Defaults 返回默认配置。
func Defaults() Config {
	return Config{
		Site:        "https://linux.sb",
		Rules:       PriceRules{SR: 30, R: 10, N: 4},
		MaxSpend:    500,
		DryRun:      true,
		ScanSec:     60,
		MaxBuyOnce:  5,
		MaxListings: 3,
		Collector:   Collector{Keep: 5, AtHour: 9, MinTip: 1},
		Listen:      "127.0.0.1:8080",
	}
}

// Normalize 纠正非法值。
func (c *Config) Normalize() {
	if c.Site == "" {
		c.Site = "https://linux.sb"
	}
	for len(c.Site) > 0 && c.Site[len(c.Site)-1] == '/' {
		c.Site = c.Site[:len(c.Site)-1]
	}
	if c.ScanSec < 30 {
		c.ScanSec = 30
	}
	if c.MaxBuyOnce < 1 {
		c.MaxBuyOnce = 1
	}
	if c.MaxListings < 1 {
		c.MaxListings = 1
	}
	if c.Collector.Keep < 0 {
		c.Collector.Keep = 0
	}
	if c.Collector.AtHour < 0 || c.Collector.AtHour > 23 {
		c.Collector.AtHour = 9
	}
	if c.Collector.MinTip < 1 {
		c.Collector.MinTip = 1
	}
	if c.Listen == "" {
		c.Listen = "127.0.0.1:8080"
	}
}

// CollectorDryRun 归集是否走 dry-run（复用全局开关）。
func (c *Config) CollectorDryRun() bool { return c.DryRun }
