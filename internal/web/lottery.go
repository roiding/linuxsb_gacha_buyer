package web

import (
	"encoding/json"
	"net/http"
	"strings"
	"unicode/utf8"

	"gacha-buyer/internal/config"
)

func (s *Server) handleLottery(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		enabled := 0
		for _, sub := range s.cfg.Subs {
			if sub.Enabled && strings.TrimSpace(sub.Username) != "" {
				enabled++
			}
		}
		writeJSON(w, map[string]any{
			"url":          s.cfg.Lottery.URL,
			"messages":     s.cfg.Lottery.Messages,
			"enabled_subs": enabled,
			"dry_run":      s.cfg.DryRun,
			"running":      s.lot.Running(),
			"records":      s.lot.Logs(300),
		})
	case http.MethodPost:
		var in struct {
			URL      string   `json:"url"`
			Messages []string `json:"messages"`
		}
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			httpError(w, http.StatusBadRequest, "请求体不是合法 JSON")
			return
		}
		normalized, err := config.NormalizeTopicURL(s.cfg.Site, in.URL)
		if err != nil {
			httpError(w, http.StatusBadRequest, err.Error())
			return
		}
		seen := map[string]bool{}
		messages := make([]string, 0, len(in.Messages))
		for _, msg := range in.Messages {
			msg = strings.TrimSpace(msg)
			if msg == "" || seen[msg] {
				continue
			}
			if utf8.RuneCountInString(msg) < 5 {
				httpError(w, http.StatusBadRequest, "每条回复语料至少需要 5 个字")
				return
			}
			seen[msg] = true
			messages = append(messages, msg)
		}
		if len(messages) == 0 {
			httpError(w, http.StatusBadRequest, "请至少保留一条回复语料")
			return
		}
		s.cfg.Lottery = config.LotteryReplyConfig{URL: normalized, Messages: messages}
		if err := config.Save(s.d, s.cfg); err != nil {
			httpError(w, http.StatusInternalServerError, "保存抽奖回复配置失败: "+err.Error())
			return
		}
		writeJSON(w, map[string]any{"ok": true})
	default:
		httpError(w, http.StatusMethodNotAllowed, "方法不允许")
	}
}

func (s *Server) handleLotteryRun(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		httpError(w, http.StatusMethodNotAllowed, "方法不允许")
		return
	}
	if s.cfg.Lottery.URL == "" {
		httpError(w, http.StatusBadRequest, "请先保存抽奖帖 URL")
		return
	}
	if len(s.cfg.Lottery.Messages) == 0 {
		httpError(w, http.StatusBadRequest, "回复语料库为空")
		return
	}
	enabled := 0
	for _, sub := range s.cfg.Subs {
		if sub.Enabled && strings.TrimSpace(sub.Username) != "" {
			enabled++
		}
	}
	if enabled == 0 {
		httpError(w, http.StatusBadRequest, "没有已启用的小号")
		return
	}
	if !s.lot.StartOnce() {
		httpError(w, http.StatusConflict, "抽奖回复任务正在运行")
		return
	}
	writeJSON(w, map[string]any{"ok": true, "message": "抽奖回复任务已开始"})
}
