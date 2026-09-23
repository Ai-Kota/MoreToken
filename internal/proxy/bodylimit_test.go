package proxy

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"moretoken/internal/config"
	"moretoken/internal/router"
)

// newLimitServer 建一个请求体上限被调到 limit 的端点，上游是 fake。
// 返回 (handler, 上游命中计数)。
func newLimitServer(t *testing.T, limit int64) (http.Handler, *int32) {
	t.Helper()
	var hits int32
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"ok":true}`))
	}))
	t.Cleanup(up.Close)

	cfg := &config.Config{Providers: []config.Provider{{
		ID: "p", BaseURL: up.URL + "/v1", Format: config.FormatOpenAI,
		Tier: config.TierFree, Keys: []string{"sk-1"},
		Models: []config.Model{{ID: "m"}},
	}}}
	s := NewServer(router.New(cfg, router.WithClient(http.DefaultClient)), cfg, nil)
	s.maxBody = limit
	return s.Handler(), &hits
}

// TestBodyLimit_OverLimit_Returns413 —— 超限必须显式 413，且**不转发上游**。
//
// 旧实现用 io.LimitReader 静默截断：超限不报错、只是少读，半个 JSON 被转发上游，
// 上游回 `unexpected end of JSON input`，网关原样透传——调用方看到的是
// "你自己发了坏 JSON"，而真凶（网关截断）在日志里一个字都没有。
// 22 万 token 就撞上，长会话必然踩。
func TestBodyLimit_OverLimit_Returns413(t *testing.T) {
	h, hits := newLimitServer(t, 1024)

	big := `{"model":"m","pad":"` + strings.Repeat("x", 2000) + `"}`
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(big))
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want 413", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "request body too large") {
		t.Errorf("响应体应说明原因，got %s", rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "1024") {
		t.Errorf("响应体应带上限值，便于调用方定位，got %s", rec.Body.String())
	}
	if got := atomic.LoadInt32(hits); got != 0 {
		t.Errorf("上游被命中 %d 次，超限请求不该被转发", got)
	}
}

// TestBodyLimit_ExactlyAtLimit_Passes 刚好到限要放行（额外多读的 1 字节不能误伤）。
func TestBodyLimit_ExactlyAtLimit_Passes(t *testing.T) {
	const limit = 1024
	h, hits := newLimitServer(t, limit)

	// 构造正好 limit 字节的 JSON
	prefix := `{"model":"m","pad":"`
	suffix := `"}`
	body := prefix + strings.Repeat("x", limit-len(prefix)-len(suffix)) + suffix
	if len(body) != limit {
		t.Fatalf("构造失败：body %d 字节，want %d", len(body), limit)
	}

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body)))

	if rec.Code != http.StatusOK {
		t.Fatalf("刚好到限应放行，got status %d body=%s", rec.Code, rec.Body.String())
	}
	if got := atomic.LoadInt32(hits); got != 1 {
		t.Errorf("上游命中 %d 次，want 1", got)
	}
}

// TestBodyLimit_OverLimitByOneByte_PassesAsRejected 超限一字节也要拒绝（边界不能差一）。
func TestBodyLimit_OverLimitByOneByte(t *testing.T) {
	const limit = 1024
	h, hits := newLimitServer(t, limit)

	prefix := `{"model":"m","pad":"`
	suffix := `"}`
	body := prefix + strings.Repeat("x", limit-len(prefix)-len(suffix)+1) + suffix
	if len(body) != limit+1 {
		t.Fatalf("构造失败：body %d 字节，want %d", len(body), limit+1)
	}

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body)))

	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("超限一字节应 413，got %d", rec.Code)
	}
	if got := atomic.LoadInt32(hits); got != 0 {
		t.Errorf("上游被命中 %d 次，不该转发", got)
	}
}

// TestBodyLimit_DefaultIsGenerous 默认上限必须显著高于旧 1 MiB——
// 否则修了半天还是 22 万 token 撞墙。
func TestBodyLimit_DefaultIsGenerous(t *testing.T) {
	if MaxBodyBytes <= 1<<20 {
		t.Fatalf("MaxBodyBytes = %d，必须显著大于旧的 1 MiB", MaxBodyBytes)
	}
	if s := NewServer(nil, &config.Config{}, nil); s.maxBody != MaxBodyBytes {
		t.Errorf("默认 maxBody = %d, want %d", s.maxBody, MaxBodyBytes)
	}
}
