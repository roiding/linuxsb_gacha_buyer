package site

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"gacha-buyer/internal/config"
)

func TestParseReplyForms(t *testing.T) {
	noCaptcha := `<form action="/reply_edit" method="post"><input type="hidden" name="_csrf" value="csrf"><input type="hidden" name="topic_id" value="15138"><textarea name="body"></textarea></form>`
	f, err := parseReplyForm(noCaptcha, 15138)
	if err != nil || f.action != "/reply_edit" || f.fields.Get("_csrf") != "csrf" || f.captchaToken != "" {
		t.Fatalf("无验证码表单解析错误: %#v %v", f, err)
	}
	withCaptcha := `<form action="/reply_edit" method="post"><input type="hidden" name="_csrf" value="csrf"><input type="hidden" name="topic_id" value="15458"><textarea name="body"></textarea><input name="native_captcha_token" value="token"><input name="native_captcha_pow" value="old"><input name="native_captcha_company" value=""></form>`
	f, err = parseReplyForm(withCaptcha, 15458)
	if err != nil || f.captchaToken != "token" || f.fields.Get("native_captcha_pow") != "old" {
		t.Fatalf("有验证码表单解析错误: %#v %v", f, err)
	}
}

func TestReplyWithoutConfirmationIsSubmittedOnly(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/topic/15138":
			_, _ = w.Write([]byte(`<form action="/reply_edit" method="post"><input type="hidden" name="_csrf" value="csrf"><input type="hidden" name="topic_id" value="15138"><textarea name="body"></textarea></form>`))
		case "/reply_edit":
			_, _ = w.Write([]byte("回复请求已提交"))
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
	res := client.Reply(server.URL+"/topic/15138", "参与本次抽奖")
	if !res.Submitted || res.Confirmed || !strings.Contains(res.Message, "未确认") {
		t.Fatalf("无 replyid 不应确认: %+v", res)
	}
}

func TestReplyWithCaptchaConfirmsReplyID(t *testing.T) {
	old := replyCaptchaDelay
	replyCaptchaDelay = false
	defer func() { replyCaptchaDelay = old }()
	token := "eyJxdWVzdGlvbiI6IjIgKyAzID0gPyIsInBvdyI6ImFiYyIsInplcm9zIjoxfQ.signature"
	var posted bool
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/topic/15458":
			_, _ = w.Write([]byte(`<form action="/reply_edit" method="post"><input type="hidden" name="_csrf" value="csrf2"><input type="hidden" name="topic_id" value="15458"><textarea name="body"></textarea><input name="native_captcha_answer"><input type="hidden" name="native_captcha_token" value="` + token + `"><input type="hidden" name="native_captcha_pow" value=""><input name="native_captcha_company"></form>`))
		case "/reply_edit":
			posted = true
			if r.Referer() != server.URL+"/topic/15458" || r.FormValue("native_captcha_answer") != "5" || r.FormValue("native_captcha_pow") == "" || r.FormValue("native_captcha_company") != "" {
				http.Error(w, "bad captcha form", http.StatusBadRequest)
				return
			}
			w.Header().Set("Location", "/topic/15458?replyid=456")
			w.WriteHeader(http.StatusSeeOther)
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
	res := client.Reply(server.URL+"/topic/15458", "祝大家抽奖好运")
	if !posted || !res.Submitted || !res.Confirmed || res.ReplyID != 456 || !res.NeedsCaptcha {
		t.Fatalf("验证码回复确认错误: %+v posted=%v", res, posted)
	}
}
