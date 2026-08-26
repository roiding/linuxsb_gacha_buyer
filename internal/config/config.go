// Package config 配置模型与默认值。全部持久化由 internal/db 的 settings 表承担。
package config

import (
	"fmt"
	"net/url"
	"regexp"
	"strings"
)

// LotteryReplyConfig 抽奖帖回复配置。
type LotteryReplyConfig struct {
	URL      string   `json:"url"`
	Messages []string `json:"messages"`
}

var topicPathRE = regexp.MustCompile(`^/topic/[1-9][0-9]*$`)

// DefaultLotteryMessages 返回默认回复语料库。
func DefaultLotteryMessages() []string {
	return []string{
		"来参加抽奖啦，祝大家都能抽到心仪的奖品！",
		"支持楼主的福利活动，祝这次抽奖顺利开奖！",
		"抽奖一时爽，希望这次好运可以降临到我身上。",
		"前排报名参加，感谢楼主准备这么好的奖品。",
		"来碰碰运气，祝楼主活动圆满，也祝大家好运。",
		"看到抽奖帖就来支持一下，期待开奖的好消息。",
		"认真参与本次抽奖，希望能够成为那个幸运儿。",
		"感谢楼主分享福利，祝大家都能开心中奖。",
		"参加一下活动，愿好运常在，开奖顺顺利利。",
		"来啦来啦，支持一下热心楼主的抽奖活动。",
		"希望这次可以抽中，先祝所有参与者好运。",
		"福利必须支持，感谢楼主带来的这次机会。",
		"报名抽奖成功，期待最后的幸运名单公布。",
		"来试试手气，祝楼主每天都有好心情。",
		"感谢举办抽奖活动，祝奖品找到合适的主人。",
		"看到活动就来参加，愿好运眷顾每一位朋友。",
		"抽奖快乐，祝本帖人气越来越旺、活动顺利。",
		"支持一下楼主，期待开奖，也祝大家好运连连。",
		"来参加本帖抽奖，希望今天能够有一点惊喜。",
		"感谢楼主慷慨分享，祝这次活动圆满结束。",
		"前排留名参与一下，祝楼主和大家都顺心。",
		"有奖活动当然要支持，期待幸运降临到我。",
		"来抽一个试试，祝所有参与的朋友都能中奖。",
		"感谢楼主提供机会，祝活动顺利进行到最后。",
		"参与一下本次福利，愿好运和惊喜一起到来。",
		"支持抽奖活动，祝楼主发帖顺利、开奖顺利。",
		"来报个名，希望能够抽到这份特别的礼物。",
		"祝大家手气爆棚，也感谢楼主带来的福利。",
		"看到这么好的活动，当然要来认真参与一下。",
		"希望这次抽奖能中，先给楼主送上祝福。",
		"参加活动碰碰运气，愿今天有一个好结果。",
		"感谢楼主分享好福利，祝所有人都开心。",
		"支持一下抽奖帖，期待开奖时的惊喜时刻。",
		"来试试今天的运气，祝活动顺利圆满完成。",
		"报名参与，愿幸运之神这次能够看见我。",
		"福利活动必须支持，祝楼主生活愉快顺心。",
		"希望能够成为幸运的一员，感谢楼主举办活动。",
		"来参加抽奖啦，祝大家都能收获好消息。",
		"支持楼主的分享，期待这次活动公平开奖。",
		"抽奖当然不能错过，祝本帖活动越来越热闹。",
		"参加一下活动，愿好运陪伴大家直到开奖。",
		"感谢楼主准备福利，祝奖品最终花落有缘人。",
		"来参与本次抽奖，希望可以给今天带来惊喜。",
		"祝楼主活动成功，也祝各位朋友抽奖顺利。",
		"看到福利就来支持，期待最后的中奖名单。",
	}
}

// NormalizeTopicURL 校验并规范化站内抽奖帖 URL。
func NormalizeTopicURL(site, raw string) (string, error) {
	site = strings.TrimRight(strings.TrimSpace(site), "/")
	if site == "" {
		site = "https://linux.sb"
	}
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Scheme == "" || u.Host == "" {
		return "", fmt.Errorf("抽奖帖 URL 不合法")
	}
	base, err := url.Parse(site)
	if err != nil || !strings.EqualFold(u.Scheme, base.Scheme) || !strings.EqualFold(u.Host, base.Host) {
		return "", fmt.Errorf("抽奖帖必须是 %s 站内链接", site)
	}
	if !topicPathRE.MatchString(u.Path) || u.RawQuery != "" || u.Fragment != "" {
		return "", fmt.Errorf("抽奖帖 URL 必须形如 %s/topic/123", site)
	}
	return site + u.Path, nil
}

// NormalizeLottery 清理抽奖配置。
func (c *Config) NormalizeLottery() {
	if c.Lottery.URL != "" {
		if normalized, err := NormalizeTopicURL(c.Site, c.Lottery.URL); err == nil {
			c.Lottery.URL = normalized
		} else {
			c.Lottery.URL = ""
		}
	}
	seen := map[string]bool{}
	messages := make([]string, 0, len(c.Lottery.Messages))
	for _, msg := range c.Lottery.Messages {
		msg = strings.TrimSpace(msg)
		if msg == "" || seen[msg] {
			continue
		}
		seen[msg] = true
		messages = append(messages, msg)
	}
	c.Lottery.Messages = messages
}

// PriceRules 按稀有度的单卡限价（积分），0 表示该稀有度不收购。
type PriceRules struct {
	SR  int `json:"sr"`
	R   int `json:"r"`
	N   int `json:"n"`
	SSR int `json:"ssr"`
	UR  int `json:"ur"`
}

// TargetRule 按名称定向收购规则（任意稀有度）。
// Price 为单卡价格上限（>0 生效）；Max 为背包最大持有数（>0 生效，0 表示不限数量）。
type TargetRule struct {
	Price int `json:"price"`
	Max   int `json:"max"`
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
	Message string `json:"message"`
	MinTip  int    `json:"min_tip"`
}

// Config 完整运行配置（内存态，由 db 加载/保存）。
type Config struct {
	Site     string `json:"site"`
	Username string `json:"username"` // 主号
	Password string `json:"password"`

	Rules      PriceRules            `json:"rules"`
	SSRPrices  map[string]int        `json:"ssr_prices,omitempty"`
	Targets    map[string]TargetRule `json:"targets,omitempty"`
	MinBalance int                   `json:"min_balance"`
	DryRun     bool                  `json:"dry_run"`
	ScanSec    int                   `json:"scan_sec"`

	Subs      []SubAccount       `json:"subs"`
	Collector Collector          `json:"collector"`
	Lottery   LotteryReplyConfig `json:"lottery"`

	Listen string `json:"listen"`
}

// Defaults 返回默认配置。
func Defaults() Config {
	return Config{
		Site:       "https://linux.sb",
		Rules:      PriceRules{SR: 30, R: 10, N: 4},
		SSRPrices:  map[string]int{},
		Targets:    map[string]TargetRule{},
		MinBalance: 0,
		DryRun:     true,
		ScanSec:    60,
		Collector:  Collector{Keep: 5, MinTip: 1},
		Lottery:    LotteryReplyConfig{Messages: DefaultLotteryMessages()},
		Listen:     "127.0.0.1:8080",
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
	if c.ScanSec < 1 {
		c.ScanSec = 1
	}
	if c.MinBalance < 0 {
		c.MinBalance = 0
	}
	if c.SSRPrices == nil {
		c.SSRPrices = map[string]int{}
	}
	for name, price := range c.SSRPrices {
		if strings.TrimSpace(name) == "" || price <= 0 {
			delete(c.SSRPrices, name)
		}
	}
	if c.Targets == nil {
		c.Targets = map[string]TargetRule{}
	}
	for name, rule := range c.Targets {
		if strings.TrimSpace(name) == "" || (rule.Price <= 0 && rule.Max <= 0) {
			delete(c.Targets, name)
			continue
		}
		if rule.Price < 0 {
			rule.Price = 0
		}
		if rule.Max < 0 {
			rule.Max = 0
		}
		c.Targets[name] = rule
	}
	if c.Collector.Keep < 0 {
		c.Collector.Keep = 0
	}
	if c.Collector.MinTip < 1 {
		c.Collector.MinTip = 1
	}
	if c.Lottery.Messages == nil {
		c.Lottery.Messages = DefaultLotteryMessages()
	}
	c.NormalizeLottery()
	if c.Listen == "" {
		c.Listen = "127.0.0.1:8080"
	}
}

// CollectorDryRun 归集是否走 dry-run（复用全局开关）。
func (c *Config) CollectorDryRun() bool { return c.DryRun }
