package proxy

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"moretoken/internal/auth"
)

// newAuthTestServer 只填中间件所需字段的 Server（同包测试可直构）。
// next 记录"是否被调到 + 传入的 auth name"。
func newAuthTestServer(t *testing.T, entries []auth.Entry) (*Server, *http.HandlerFunc, *string) {
	t.Helper()
	s := &Server{declog: NewDecisionLog(16)}
	if entries != nil {
		s.auth = auth.InMemoryStore(entries)
	}
	var gotName string
	next := http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		gotName = AuthNameFromContext(req.Context())
		w.WriteHeader(http.StatusOK)
	})
	return s, &next, &gotName
}

// entry expiresIn != 0 → 设 Expires（负值 = 已过期，用于过期格）；0 = 永不过期。
func entry(name, plain string, expiresIn time.Duration) auth.Entry {
	e := auth.Entry{Name: name, Hash: auth.Hash(plain), Created: time.Now()}
	if expiresIn != 0 {
		exp := time.Now().Add(expiresIn)
		e.Expires = &exp
	}
	return e
}

func doReq(h http.Handler, path string, headers map[string]string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader("{}"))
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	return w
}

// TestAuthMiddleware_ValidBothHeaders 双协议头各通过（矩阵"双协议头/正常"格）。
func TestAuthMiddleware_ValidBothHeaders(t *testing.T) {
	for _, tc := range []struct {
		header, value string
	}{
		{"Authorization", "Bearer mt_good-token"},
		{"x-api-key", "mt_good-token"},
	} {
		s, next, name := newAuthTestServer(t, []auth.Entry{entry("agent", "mt_good-token", time.Hour)})
		h := s.authMiddleware(*next)
		w := doReq(h, "/v1/chat/completions", map[string]string{tc.header: tc.value})
		if w.Code != http.StatusOK {
			t.Fatalf("%s: code = %d, want 200 (body=%s)", tc.header, w.Code, w.Body.String())
		}
		if *name != "agent" {
			t.Fatalf("%s: context auth name = %q, want agent", tc.header, *name)
		}
	}
}

// TestAuthMiddleware_Reject 无头/错 token/畸形头 → 401（矩阵"空/错误"格）。
func TestAuthMiddleware_Reject(t *testing.T) {
	cases := []struct {
		label   string
		headers map[string]string
	}{
		{"无头", nil},
		{"错 token", map[string]string{"Authorization": "Bearer mt_wrong"}},
		{"畸形头（无 Bearer 前缀）", map[string]string{"Authorization": "mt_good-token"}},
		{"空 x-api-key", map[string]string{"x-api-key": "  "}},
	}
	for _, tc := range cases {
		s, next, _ := newAuthTestServer(t, []auth.Entry{entry("agent", "mt_good-token", time.Hour)})
		h := s.authMiddleware(*next)
		w := doReq(h, "/v1/chat/completions", tc.headers)
		if w.Code != http.StatusUnauthorized {
			t.Fatalf("%s: code = %d, want 401", tc.label, w.Code)
		}
	}
}

// TestAuthMiddleware_BothHeadersAuthorizationWins 两头都带且不一致 → 取 Authorization。
func TestAuthMiddleware_BothHeadersAuthorizationWins(t *testing.T) {
	s, next, name := newAuthTestServer(t, []auth.Entry{
		entry("bearer-agent", "mt_good-token", time.Hour),
		entry("apikey-agent", "mt_other-token", time.Hour),
	})
	h := s.authMiddleware(*next)
	w := doReq(h, "/v1/chat/completions", map[string]string{
		"Authorization": "Bearer mt_good-token",
		"x-api-key":     "mt_stale-token", // 故意送第三个值：若误取 x-api-key 必 401
	})
	if w.Code != http.StatusOK || *name != "bearer-agent" {
		t.Fatalf("code=%d name=%q, want 200/bearer-agent", w.Code, *name)
	}
}

// TestAuthMiddleware_HealthExempt /health 无 token 直通（TH8：dev-fleet 探针永不被挡）。
func TestAuthMiddleware_HealthExempt(t *testing.T) {
	s, next, _ := newAuthTestServer(t, []auth.Entry{entry("agent", "mt_good-token", time.Hour)})
	h := s.authMiddleware(*next)
	w := doReq(h, "/health", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("/health code = %d, want 200（豁免）", w.Code)
	}
}

// TestAuthMiddleware_401BodyShape 401 body 按端点协议成形（SDK 认自家错误形状）。
func TestAuthMiddleware_401BodyShape(t *testing.T) {
	s, next, _ := newAuthTestServer(t, []auth.Entry{entry("agent", "mt_good-token", time.Hour)})
	h := s.authMiddleware(*next)

	// anthropic 端点 → type/error 结构
	w := doReq(h, "/v1/messages", nil)
	var anth struct {
		Type  string `json:"type"`
		Error struct {
			Type string `json:"type"`
		} `json:"error"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &anth); err != nil {
		t.Fatalf("/v1/messages 401 body 非法 JSON: %v (%s)", err, w.Body.String())
	}
	if anth.Type != "error" || anth.Error.Type != "authentication_error" {
		t.Fatalf("anthropic 401 形状不对: %s", w.Body.String())
	}

	// openai 端点 → error.message/type/code 结构
	w = doReq(h, "/v1/chat/completions", nil)
	var oai struct {
		Error struct {
			Message string `json:"message"`
			Type    string `json:"type"`
			Code    string `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &oai); err != nil {
		t.Fatalf("openai 401 body 非法 JSON: %v", err)
	}
	if oai.Error.Code != "invalid_api_key" {
		t.Fatalf("openai 401 形状不对: %s", w.Body.String())
	}
}

// TestAuthMiddleware_RejectRecorded 拒绝必留痕（T-016 教训）：401 进决策日志。
func TestAuthMiddleware_RejectRecorded(t *testing.T) {
	s, next, _ := newAuthTestServer(t, []auth.Entry{entry("agent", "mt_good-token", time.Hour)})
	h := s.authMiddleware(*next)
	doReq(h, "/v1/models", map[string]string{"Authorization": "Bearer mt_wrong"})

	ents := s.declog.Recent(10)
	if len(ents) != 1 {
		t.Fatalf("决策日志 = %d 条, want 1", len(ents))
	}
	e := ents[0]
	if e.ProviderID != "auth" || e.Status != http.StatusUnauthorized {
		t.Fatalf("留痕 = (%s,%d), want (auth,401)", e.ProviderID, e.Status)
	}
	if !strings.Contains(e.Reason, "/v1/models") || !strings.Contains(e.Reason, "Authorization") {
		t.Fatalf("留痕应含 path 与来源头: %q", e.Reason)
	}
}

// TestAuthMiddleware_ExpiredReason 过期 token 的 401 说清"吊销或过期"（排查不用猜）。
func TestAuthMiddleware_ExpiredReason(t *testing.T) {
	s, next, _ := newAuthTestServer(t, []auth.Entry{entry("old-agent", "mt_expired-token", -time.Hour)})
	h := s.authMiddleware(*next)
	w := doReq(h, "/v1/chat/completions", map[string]string{"Authorization": "Bearer mt_expired-token"})
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("code = %d, want 401", w.Code)
	}
	ents := s.declog.Recent(10)
	if len(ents) != 1 || !strings.Contains(ents[0].Reason, "old-agent") {
		t.Fatalf("留痕应含命中的 name: %+v", ents)
	}
}

// TestAuthMiddleware_NilStoreNeverMounted 未配置鉴权 → Handler() 不挂中间件（兼容格）。
func TestAuthMiddleware_NilStoreNeverMounted(t *testing.T) {
	s, next, _ := newAuthTestServer(t, nil) // auth == nil
	if s.authEnabled() {
		t.Fatal("nil store 应为未启用")
	}
	// 直接打 next 模拟"原样返回 mux"的效果
	w := doReq(*next, "/v1/chat/completions", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("无鉴权路径 code = %d, want 200", w.Code)
	}
}

// TestExtractToken 头解析的单点行为（含 Bearer 大小写边界：只认标准写法）。
func TestExtractToken(t *testing.T) {
	cases := []struct {
		headers  map[string]string
		wantTok  string
		wantHead string
	}{
		{map[string]string{"Authorization": "Bearer mt_a"}, "mt_a", "Authorization"},
		{map[string]string{"Authorization": "Bearer  mt_a "}, "mt_a", "Authorization"}, // 多空格/尾空格
		{map[string]string{"Authorization": "Basic xyz"}, "", "Authorization"},
		{map[string]string{"x-api-key": "mt_b"}, "mt_b", "x-api-key"},
		{nil, "", ""},
	}
	for _, tc := range cases {
		req := httptest.NewRequest(http.MethodGet, "/x", nil)
		for k, v := range tc.headers {
			req.Header.Set(k, v)
		}
		tok, head := extractToken(req)
		if tok != tc.wantTok || head != tc.wantHead {
			t.Fatalf("%v → (%q,%q), want (%q,%q)", tc.headers, tok, head, tc.wantTok, tc.wantHead)
		}
	}
}
