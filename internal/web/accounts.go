package web

import (
	"encoding/json"
	"net/http"

	"gacha-buyer/internal/config"
)

// handleAccounts GET 列出脱敏账号状态；POST 维护主号、小号与归集设置。
func (s *Server) handleAccounts(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		writeJSON(w, map[string]any{
			"accounts": s.mgr.Snapshot(),
			"collector": map[string]any{
				"topic_id": s.cfg.Collector.TopicID,
				"keep":     s.cfg.Collector.Keep,
				"at_hour":  s.cfg.Collector.AtHour,
				"message":  s.cfg.Collector.Message,
			},
		})

	case http.MethodPost:
		var in struct {
			MainUsername *string            `json:"main_username,omitempty"`
			MainPassword *string            `json:"main_password,omitempty"`
			Action       string             `json:"action,omitempty"`
			ID           int                `json:"id,omitempty"`
			Sub          *config.SubAccount `json:"sub,omitempty"`
			Collector    *struct {
				TopicID int    `json:"topic_id"`
				Keep    int    `json:"keep"`
				AtHour  int    `json:"at_hour"`
				Message string `json:"message"`
			} `json:"collector,omitempty"`
		}
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			httpError(w, 400, "请求体不是合法 JSON")
			return
		}

		if in.MainUsername != nil || in.MainPassword != nil {
			username := s.cfg.Username
			if in.MainUsername != nil {
				username = *in.MainUsername
			}
			if username == "" {
				httpError(w, 400, "主号用户名不能为空")
				return
			}
			password := s.cfg.Password
			if in.MainPassword != nil && *in.MainPassword != "" {
				password = *in.MainPassword
			}
			if password == "" {
				httpError(w, 400, "首次设置主号时必须填写密码")
				return
			}
			s.cfg.Username, s.cfg.Password = username, password
			s.eng.InvalidateSession()
		}
		if in.Collector != nil {
			s.cfg.Collector.TopicID = in.Collector.TopicID
			s.cfg.Collector.Keep = in.Collector.Keep
			s.cfg.Collector.AtHour = in.Collector.AtHour
			s.cfg.Collector.Message = in.Collector.Message
		}

		switch in.Action {
		case "":
		case "add":
			if in.Sub == nil || in.Sub.Username == "" || in.Sub.Password == "" {
				httpError(w, 400, "小号需要用户名与密码")
				return
			}
			if s.findSub(in.Sub.Username) >= 0 {
				httpError(w, 409, "该小号已存在")
				return
			}
			in.Sub.Enabled = true
			s.cfg.Subs = append(s.cfg.Subs, *in.Sub)
		case "delete", "toggle":
			row, err := s.d.GetAccountByID(in.ID)
			if err != nil || row.Role != "sub" {
				httpError(w, 404, "小号不存在")
				return
			}
			idx := s.findSub(row.Username)
			if idx < 0 {
				httpError(w, 404, "小号配置不存在")
				return
			}
			if in.Action == "delete" {
				s.cfg.Subs = append(s.cfg.Subs[:idx], s.cfg.Subs[idx+1:]...)
			} else {
				s.cfg.Subs[idx].Enabled = !s.cfg.Subs[idx].Enabled
			}
		default:
			httpError(w, 400, "未知 action: "+in.Action)
			return
		}

		s.cfg.Normalize()
		if err := config.Save(s.d, s.cfg); err != nil {
			httpError(w, 500, "保存失败: "+err.Error())
			return
		}
		s.mgr.Resync()
		writeJSON(w, map[string]any{"ok": true})
	default:
		httpError(w, 405, "方法不允许")
	}
}

func (s *Server) findSub(username string) int {
	for i, sub := range s.cfg.Subs {
		if sub.Username == username {
			return i
		}
	}
	return -1
}

// handleAccountRecover POST {role, username} 人工恢复异常账号。
func (s *Server) handleAccountRecover(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		httpError(w, 405, "方法不允许")
		return
	}
	var in struct {
		ID int `json:"id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		httpError(w, 400, "请求体不是合法 JSON")
		return
	}
	if err := s.mgr.RecoverByID(in.ID); err != nil {
		writeJSON(w, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	writeJSON(w, map[string]any{"ok": true})
}

// handleAccountLogout POST {role, username} 让该账号退出站点登录并清空本地会话。
func (s *Server) handleAccountLogout(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		httpError(w, 405, "方法不允许")
		return
	}
	var in struct {
		ID int `json:"id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		httpError(w, 400, "请求体不是合法 JSON")
		return
	}
	if err := s.mgr.LogoutByID(in.ID); err != nil {
		writeJSON(w, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	writeJSON(w, map[string]any{"ok": true})
}

// handlePatrolOnce POST 立即执行一次全部账号巡检。
func (s *Server) handlePatrolOnce(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		httpError(w, 405, "方法不允许")
		return
	}
	go s.mgr.PatrolOnce()
	writeJSON(w, map[string]any{"ok": true, "message": "巡检已开始"})
}

// handleTransfers GET 归集记录。
func (s *Server) handleTransfers(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, map[string]any{
		"transfers": s.col.Transfers(),
		"status":    s.col.Snapshot(),
	})
}

// handleCollectorRun POST 立即执行一轮归集。
func (s *Server) handleCollectorRun(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		httpError(w, 405, "方法不允许")
		return
	}
	go func() { s.col.RunOnce(true) }()
	writeJSON(w, map[string]any{"ok": true, "message": "归集已开始，请稍后刷新记录"})
}
