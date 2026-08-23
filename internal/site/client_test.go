package site

import (
	"strings"
	"testing"
)

func TestSolveCaptcha(t *testing.T) {
	cases := []struct {
		in, want string
		ok       bool
	}{
		{"9 - 6 = ?", "3", true},
		{"2 × 4 = ?", "8", true},
		{"12 + 30 = ?", "42", true},
		{"2 x 4 = ?", "8", true},
		{"8 ÷ 2 = ?", "4", true},
		{"２ × ４ = ?", "", false}, // 全角数字不支持（站点未见过）
		{"2 × 4 ＝ ？", "8", true}, // 全角等号问号
		{"abc", "", false},
		{"7 ÷ 2 = ?", "", false},
	}
	for _, c := range cases {
		got, err := SolveCaptcha(c.in)
		if c.ok && (err != nil || got != c.want) {
			t.Errorf("SolveCaptcha(%q) = %q, %v; want %q", c.in, got, err, c.want)
		}
		if !c.ok && err == nil {
			t.Errorf("SolveCaptcha(%q) 应失败，得到 %q", c.in, got)
		}
	}
}

func TestSolvePoW(t *testing.T) {
	// prefix 使 nonce=0 即满足 1 个前导零，验证基本正确性
	nonce, err := SolvePoW("f4407ee3985da9f6", 3)
	if err != nil {
		t.Fatalf("SolvePoW: %v", err)
	}
	if nonce == "" || strings.ContainsAny(nonce, "ghijklmnopqrstuvwxyz") {
		t.Fatalf("nonce 非法: %q", nonce)
	}
	if _, err := SolvePoW("x", 9); err == nil {
		t.Fatal("难度 9 应被拒绝")
	}
}

func TestTokenPayload(t *testing.T) {
	// 站点两段式：{"question":"2 × 4 = ?","pow":"abc","zeros":3}.signature
	tok := "eyJxdWVzdGlvbiI6IjIgw5cgNCA9ID8iLCJwb3ciOiJhYmMiLCJ6ZXJvcyI6M30." +
		"db757b20b3fd5d1d9cd13f2892a4b547dcd622aec1542bc0a0ca5a887c1cdcbe"
	p := tokenPayload(tok)
	if q := jsonStr(p, "question"); q != "2 × 4 = ?" {
		t.Errorf("question = %q", q)
	}
	if z := jsonInt(p, "zeros"); z != 3 {
		t.Errorf("zeros = %d", z)
	}
	if pw := jsonStr(p, "pow"); pw != "abc" {
		t.Errorf("pow = %q", pw)
	}
	// 标准 JWT 三段式兼容
	jwt := "h.eyJ6ZXJvcyI6NX0.sig"
	if z := jsonInt(tokenPayload(jwt), "zeros"); z != 5 {
		t.Errorf("jwt zeros = %d", z)
	}
}

func TestFindTrapName(t *testing.T) {
	page := `<label class="native-captcha-trap" aria-hidden="true">公司<input type="text" name="native_captcha_company" tabindex="-1"></label>`
	if n := findTrapName(page); n != "native_captcha_company" {
		t.Errorf("trap = %q", n)
	}
	if n := findTrapName("<p>none</p>"); n != "" {
		t.Errorf("应返回空，得到 %q", n)
	}
}
