package web

import (
	"encoding/json"
	"net/http"

	"gacha-buyer/internal/market"
)

// handleMarketPublish POST 批量上架：按稀有度分类 + 统一单价 + 时长。
func (s *Server) handleMarketPublish(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		httpError(w, 405, "方法不允许")
		return
	}
	var in struct {
		Rarities      []string `json:"rarities"`
		UnitPrice     int      `json:"unit_price"`
		DurationHours int      `json:"duration_hours"`
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		httpError(w, 400, "请求体不是合法 JSON")
		return
	}
	var rarities []market.Rarity
	for _, x := range in.Rarities {
		switch market.Rarity(x) {
		case market.N, market.R, market.SR, market.SSR, market.UR:
			rarities = append(rarities, market.Rarity(x))
		}
	}
	res := s.eng.BulkPublish(rarities, in.UnitPrice, in.DurationHours)
	writeJSON(w, res)
}

// handleMarketCancel POST 批量下架当前账号全部在售挂牌。
func (s *Server) handleMarketCancel(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		httpError(w, 405, "方法不允许")
		return
	}
	writeJSON(w, s.eng.BulkCancel())
}
