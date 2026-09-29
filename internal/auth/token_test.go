package auth

import (
	"strings"
	"testing"
)

// TestGenToken_Shape mt_ 前缀 + 43 字符 base62 正文（矩阵"GenToken/正常"格）。
func TestGenToken_Shape(t *testing.T) {
	tok, err := GenToken()
	if err != nil {
		t.Fatalf("GenToken: %v", err)
	}
	if !strings.HasPrefix(tok, TokenPrefix) {
		t.Fatalf("前缀 = %q, want %q", tok[:3], TokenPrefix)
	}
	body := strings.TrimPrefix(tok, TokenPrefix)
	if len(body) != tokenBodyLen {
		t.Fatalf("正文长度 = %d, want %d", len(body), tokenBodyLen)
	}
	for _, c := range body {
		if !strings.ContainsRune(b62Alphabet, c) {
			t.Fatalf("正文含非 base62 字符 %q: %s", c, tok)
		}
	}
}

// TestGenToken_Unique 两次生成不重复（矩阵"GenToken/正常"格：熵源真的在出随机数）。
func TestGenToken_Unique(t *testing.T) {
	seen := make(map[string]bool, 64)
	for i := 0; i < 64; i++ {
		tok, err := GenToken()
		if err != nil {
			t.Fatalf("GenToken: %v", err)
		}
		if seen[tok] {
			t.Fatalf("第 %d 次生成重复: %s", i, tok)
		}
		seen[tok] = true
	}
}

// TestGenToken_NoModuloBias 字符分布粗检：base62 转换无偏（每字符期望 64*43/62 ≈ 44 次，
// 放宽到 [10,120] 抓"某些字符从不出现/畸多"这类实现错，不做严格卡方）。
func TestGenToken_NoModuloBias(t *testing.T) {
	counts := make(map[rune]int)
	for i := 0; i < 64; i++ {
		tok, _ := GenToken()
		for _, c := range strings.TrimPrefix(tok, TokenPrefix) {
			counts[c]++
		}
	}
	for _, c := range b62Alphabet {
		if counts[c] < 10 || counts[c] > 120 {
			t.Fatalf("字符 %q 出现 %d 次，分布异常（期望 ~44）", c, counts[c])
		}
	}
}

// TestHash_Deterministic 同明文同哈希、异明文异哈希（校验面的根基）。
func TestHash_Deterministic(t *testing.T) {
	if Hash("mt_a") != Hash("mt_a") {
		t.Fatal("同明文哈希不一致")
	}
	if Hash("mt_a") == Hash("mt_b") {
		t.Fatal("异明文哈希相同")
	}
	if len(Hash("x")) != 64 {
		t.Fatalf("sha256 hex 长度 = %d, want 64", len(Hash("x")))
	}
}

// TestDisplayPrefix 前 8 字符、不可反推（矩阵"识别"辅助格）。
func TestDisplayPrefix(t *testing.T) {
	if p := DisplayPrefix("mt_abcdefghij"); p != "mt_abcde" {
		t.Fatalf("DisplayPrefix = %q, want mt_abcde", p)
	}
	if p := DisplayPrefix("short"); p != "short" {
		t.Fatalf("短串应原样返回, got %q", p)
	}
}
