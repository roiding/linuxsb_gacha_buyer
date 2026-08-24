package site

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"gacha-buyer/internal/config"
)

func TestDonateRequiresConfirmedBalanceChange(t *testing.T) {
	points := 100
	postCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/gacha_profile":
			w.WriteHeader(http.StatusOK)
			_, _ = fmt.Fprintf(w, "积分 %d", points)
		case "/donate":
			if r.Method == http.MethodGet {
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write([]byte(`<form><input name="_csrf" value="` + strings.Repeat("a", 64) + `"><input name="request_key" value="12345"></form>`))
				return
			}
			postCount++
			if postCount == 2 {
				points -= 20
			}
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`<div class="alert">打赏请求已提交</div>`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	cfg := config.Defaults()
	cfg.Site = server.URL
	client, err := NewClient(&cfg, nil)
	if err != nil {
		t.Fatal(err)
	}

	result := client.Donate(42, 20, "测试")
	if result.OK || !result.Pending || !result.Submitted {
		t.Fatalf("明确提交但余额未变化应进入待核验: %+v", result)
	}

	points = 80
	result = client.Donate(42, 20, "测试")
	if !result.OK || !result.Submitted || result.PointsBefore != 80 || result.PointsAfter != 60 {
		t.Fatalf("应按余额扣减确认成功: %+v", result)
	}
}

func TestDonateHardConditionIsNotRetryable(t *testing.T) {
	for _, message := range []string{"注册未满3天，暂不能打赏", "账号注册时间不足三天", "当前不满足活动资格"} {
		if isDonateRetryable(message) {
			t.Fatalf("硬条件失败不应重试: %s", message)
		}
	}
	for _, message := range []string{"系统暂时错误", "服务不可用，请稍后再试"} {
		if !isDonateRetryable(message) {
			t.Fatalf("临时错误应允许重试: %s", message)
		}
	}
}
