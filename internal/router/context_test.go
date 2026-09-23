package router

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"moretoken/internal/config"
	"moretoken/internal/provider"
)

// TestRoute_ContextTooLong_FailsFastWithoutRetry —— 确定性失败**不许重试**。
//
// 血账（2026-09-22）：某会话上下文 994166 token（上限 524288），它一直重发同一个请求。
// 旧行为是每个候选先扛 60–80 秒才回，试完两三个候选再给 503 —— 用户等了两三分钟，
// 拿到的还是一个**注定**的失败。上下文是**请求**的属性，换 provider 也是同一个答案。
//
// 【反证】把 Route 里 FailContextTooLong 那个分支删掉 ⇒ 上游会被打两次、本条变红。
func TestRoute_ContextTooLong_FailsFastWithoutRetry(t *testing.T) {
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&hits, 1)
		w.WriteHeader(http.StatusBadGateway)
		w.Write([]byte(`ContextWindowExceededError: input (994166 tokens) is longer than the model's context length (524288 tokens)`))
	}))
	t.Cleanup(srv.Close)

	cfg := &config.Config{Providers: []config.Provider{
		{ID: "a", BaseURL: srv.URL, Format: config.FormatAnthropic, Tier: config.TierFree,
			Keys: []string{"k"}, Models: []config.Model{{ID: "m"}}},
		{ID: "b", BaseURL: srv.URL, Format: config.FormatAnthropic, Tier: config.TierFree,
			Keys: []string{"k"}, Models: []config.Model{{ID: "m"}}},
		{ID: "c", BaseURL: srv.URL, Format: config.FormatAnthropic, Tier: config.TierFree,
			Keys: []string{"k"}, Models: []config.Model{{ID: "m"}}},
	}}
	r := New(cfg, WithClient(http.DefaultClient))

	_, err := r.Route(context.Background(), RouteRequest{
		Format: config.FormatAnthropic, Body: []byte(`{"model":"m","messages":[]}`),
		Kind: provider.NonStreaming,
	})
	if !errors.Is(err, ErrContextTooLong) {
		t.Fatalf("err = %v，必须是 ErrContextTooLong（proxy 靠它给出可操作提示）", err)
	}
	if got := atomic.LoadInt32(&hits); got != 1 {
		t.Errorf("上游被打了 %d 次，want 1 —— 确定性失败不该重试", got)
	}
}
