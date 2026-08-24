package site

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"gacha-buyer/internal/config"
	"gacha-buyer/internal/market"
)

func TestBuyRequiresConfirmedBalanceChange(t *testing.T) {
	points := 100
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/gacha_market_buy":
			w.Header().Set("Location", "/gacha_market")
			w.WriteHeader(http.StatusSeeOther)
		case "/gacha_market":
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`<article class="gacha-market-card"></article>`))
		case "/gacha_profile":
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("积分 " + itoa(points)))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	cfg := config.Defaults()
	cfg.Site = srv.URL
	c, err := NewClient(&cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	listing := market.Listing{ListingID: 1, Name: "常客", Price: 20, Remain: 2, CSRF: "csrf"}
	res := c.Buy(listing, 2, 100)
	if !res.Submitted || res.OK {
		t.Fatalf("未扣款不应确认成交: %+v", res)
	}

	points = 60
	res = c.Buy(listing, 2, 100)
	if !res.Submitted || !res.OK || res.Qty != 2 || res.Cost != 40 {
		t.Fatalf("扣款后应确认成交: %+v", res)
	}
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	buf := make([]byte, 0, 12)
	for n > 0 {
		buf = append([]byte{byte('0' + n%10)}, buf...)
		n /= 10
	}
	return string(buf)
}
