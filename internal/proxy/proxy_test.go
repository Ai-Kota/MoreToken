package proxy

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"moretoken/internal/config"
	"moretoken/internal/metrics"
	"moretoken/internal/provider"
	"moretoken/internal/router"
)

// newTestServer 构造 fake 上游 + Router + proxy.Server。
// sse=true 时上游回 SSE 流式块；否则回 JSON（upstream 内容）。
func newTestServer(t *testing.T, upstream string, sse bool) (*Server, *httptest.Server) {
	t.Helper()
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if sse {
			w.Header().Set("Content-Type", "text/event-stream")
			if fl, ok := w.(http.Flusher); ok {
				fl.Flush()
			}
			io.WriteString(w, "data: {\"type\":\"content_block_delta\"}\n\n")
			return
		}
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, upstream)
	}))
	t.Cleanup(up.Close)

	cfg := &config.Config{Providers: []config.Provider{
		{ID: "up", BaseURL: up.URL + "/v1", Format: "openai", Tier: "free",
			Keys: []string{"sk"}, Models: []config.Model{{ID: "m-flash", Name: "M Flash"}}},
	}}
	r := router.New(cfg, router.WithClient(http.DefaultClient))
	log := NewDecisionLog(10)
	return NewServer(r, cfg, log), up
}

// TestEndpoint_NonStreaming_RoundTrip —— 端点非流式完整往返（Claude Code 形状请求）。
func TestEndpoint_NonStreaming_RoundTrip(t *testing.T) {
	srv, _ := newTestServer(t, `{"choices":[{"message":{"content":"hi"}}]}`, false)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	resp, err := http.Post(ts.URL+"/v1/chat/completions", "application/json",
		strings.NewReader(`{"model":"m-flash","stream":false,"messages":[{"role":"user","content":"x"}]}`))
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Errorf("status = %d, want 200", resp.StatusCode)
	}
}

// TestEndpoint_Streaming_SSETransparent —— 流式端点透传 SSE 块。
func TestEndpoint_Streaming_SSETransparent(t *testing.T) {
	srv, _ := newTestServer(t, `{}`, true)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	req, _ := http.NewRequest("POST", ts.URL+"/v1/chat/completions",
		strings.NewReader(`{"model":"m-flash","stream":true}`))
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("do: %v", err)
	}
	defer resp.Body.Close()
	if ct := resp.Header.Get("Content-Type"); ct != "text/event-stream" {
		t.Errorf("content-type = %q, want text/event-stream", ct)
	}
	body := make([]byte, 1024)
	n, _ := resp.Body.Read(body)
	if !strings.Contains(string(body[:n]), "data:") {
		t.Errorf("stream body missing data: line, got %q", body[:n])
	}
}

// TestEndpoint_Models —— /v1/models 按格式过滤。
func TestEndpoint_Models(t *testing.T) {
	srv, _ := newTestServer(t, `{}`, false)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/v1/models?format=openai")
	if err != nil {
		t.Fatalf("get models: %v", err)
	}
	defer resp.Body.Close()
	buf := make([]byte, 1024)
	n, _ := resp.Body.Read(buf)
	if !strings.Contains(string(buf[:n]), "m-flash") {
		t.Errorf("models missing m-flash: %s", buf[:n])
	}
}

// TestEndpoint_Health —— /health 返回池状态。
func TestEndpoint_Health(t *testing.T) {
	srv, _ := newTestServer(t, `{}`, false)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/health")
	if err != nil {
		t.Fatalf("get health: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Errorf("health status = %d", resp.StatusCode)
	}
	buf := make([]byte, 2048)
	n, _ := resp.Body.Read(buf)
	if !strings.Contains(string(buf[:n]), `"provider"`) {
		t.Errorf("health body missing provider: %s", buf[:n])
	}
}

// TestEndpoint_Exhausted503_IsRecorded —— 503 也必须进决策日志（T-016）。
//
// 这条路径是"没有响应"的那一类结果，请求方日志里只剩一句 503。不记的话，
// 事故现场在 /decisions 里就是一段**空白**——2026-09-20 15:31:49–15:34:49 正是如此，
// 全靠翻档前后恰好有流量才反推出机制。此前该分支的注释写着"决策日志已留痕"，
// 但代码在 Record 之前就 return 了。
func TestEndpoint_Exhausted503_IsRecorded(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadGateway) // 5xx → 可重试 → 候选试遍 → 耗尽
	}))
	t.Cleanup(up.Close)

	cfg := &config.Config{Providers: []config.Provider{
		{ID: "up", BaseURL: up.URL + "/v1", Format: "openai", Tier: "free",
			Keys: []string{"sk"}, Models: []config.Model{{ID: "m", Kinds: []string{"reasoning"}}}},
	}}
	log := NewDecisionLog(10)
	srv := NewServer(router.New(cfg, router.WithClient(http.DefaultClient)), cfg, log)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	resp, err := http.Post(ts.URL+"/v1/chat/completions", "application/json",
		strings.NewReader(`{"model":"auto:reasoning","messages":[{"role":"user","content":"x"}]}`))
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", resp.StatusCode)
	}

	rec := log.Recent(0)
	if len(rec) != 1 {
		t.Fatalf("决策日志条数 = %d, want 1（503 必须留痕，否则事故现场是空白）", len(rec))
	}
	e := rec[0]
	if e.Status != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want 503", e.Status)
	}
	if e.ProviderID != "router" {
		t.Errorf("provider_id = %q, want router（没有任何 provider 服务成功）", e.ProviderID)
	}
	if !strings.Contains(e.Reason, "exhausted") {
		t.Errorf("reason = %q，应含 exhausted（能看出候选试遍且全挂）", e.Reason)
	}
	if e.Want != "auto:reasoning" {
		t.Errorf("want = %q, want auto:reasoning（要能看出是哪个模型被拒）", e.Want)
	}
	// 逐个候选的失败原因必须落进日志——聚合原因回答不了"是谁坏的、怎么坏的"。
	if len(e.Attempts) != 1 {
		t.Fatalf("attempts = %+v，want 1 条（这个配置只有一个候选）", e.Attempts)
	}
	if e.Attempts[0].Provider != "up" || e.Attempts[0].Kind != "upstream" {
		t.Errorf("attempts[0] = %+v，want provider=up kind=upstream", e.Attempts[0])
	}
	if e.Attempts[0].Status != http.StatusBadGateway {
		t.Errorf("attempts[0].status = %d，want 502（上游原文要留下）", e.Attempts[0].Status)
	}
}

// TestEndpoint_PlainAuto_NotTurnedIntoKindAny —— 回归：`model:"auto"`（**不带类型**）必须能路由。
//
// 血账（2026-09-21）：接指标时为给维度起名，就地写了 `wantKind = "any"`——
// 而 wantKind 同时是传给路由器的参数。于是路由去找"持有 kind=any 的 provider"，
// 谁也持有不了 ⇒ 候选为空 ⇒ 503 `no anthropic provider offers kind "any"`。
//
// 生产表现：`auto` 每 ~4 分钟失败一次、持续两小时（23 条 503），
// 而且**聚合原因看不出是网关自己造的**——它长得像配置缺失。
//
// 【反证】把 proxy.go 里 label 那段改回 `wantKind = "any"` ⇒ 本条变红。
func TestEndpoint_PlainAuto_NotTurnedIntoKindAny(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte(`{"content":"ok"}`))
	}))
	t.Cleanup(up.Close)
	// 两种格式各一个 provider —— 生产上挂的是 anthropic 那条，所以两个都要覆盖。
	cfg := &config.Config{Providers: []config.Provider{
		{ID: "up-oai", BaseURL: up.URL, Format: config.FormatOpenAI, Tier: config.TierFree,
			Keys: []string{"k"}, Models: []config.Model{{ID: "m"}}},
		{ID: "up-anth", BaseURL: up.URL, Format: config.FormatAnthropic, Tier: config.TierFree,
			Keys: []string{"k"}, Models: []config.Model{{ID: "m"}}},
	}}
	srv := NewServer(router.New(cfg, router.WithClient(http.DefaultClient)), cfg, NewDecisionLog(8))
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	// 不带 kind 的 "auto"：必须落到某个具体模型，而不是报"没人提供 kind=any"
	for _, path := range []string{"/v1/chat/completions", "/v1/messages"} {
		resp, err := http.Post(ts.URL+path, "application/json",
			strings.NewReader(`{"model":"auto","messages":[{"role":"user","content":"x"}]}`))
		if err != nil {
			t.Fatalf("%s: %v", path, err)
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != 200 {
			t.Errorf("%s 的 `auto` 应能路由，实际 %d：%s", path, resp.StatusCode, body)
		}
		if strings.Contains(string(body), "offers kind") {
			t.Errorf("%s：「auto」（无类型）被错当成要求某个 kind：%s", path, body)
		}
	}
}

// TestEndpoint_NoteworthySuccessCarriesElapsedAndBody —— 超阈值的**成功**请求要留痕。
//
// 为什么成功也要记：那次头超时的失败**无法主动复现**（要"大 body + 上游繁忙"同时成立，
// 后者不可控），只能等它自然发生。而如果成功路径什么都不记，
// "没再出现 503" 就分不清是**修好了**还是**没触发**。
//
// 判据：出现一条 `elapsed≈120s + body≈1.5MB + status:200` ⇒ 旧代码下它必然 503。
func TestEndpoint_NoteworthySuccessCarriesElapsedAndBody(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		time.Sleep(25 * time.Millisecond) // 把它拖到"慢"
		w.Write([]byte(`{"ok":1}`))
	}))
	t.Cleanup(up.Close)

	cfg := &config.Config{Providers: []config.Provider{
		{ID: "up", BaseURL: up.URL + "/v1", Format: config.FormatOpenAI, Tier: config.TierFree,
			Keys: []string{"k"}, Models: []config.Model{{ID: "m"}}},
	}}
	log := NewDecisionLog(8)
	srv := NewServer(router.New(cfg, router.WithClient(http.DefaultClient)), cfg, log)
	srv.slowThreshold = 10 * time.Millisecond // 测试里调小，免得真等 60 秒
	srv.largeThreshold = 16                   // body 很小，16 字节就够触发
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	resp, err := http.Post(ts.URL+"/v1/chat/completions", "application/json",
		strings.NewReader(`{"model":"m","messages":[]}`))
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}

	rec := log.Recent(0)
	if len(rec) != 1 {
		t.Fatalf("决策日志 %d 条，want 1", len(rec))
	}
	if rec[0].ElapsedMs <= 0 {
		t.Error("超过慢阈值的成功请求必须记 elapsed_ms —— 否则无法证明修复生效")
	}
	if rec[0].BodyBytes <= 0 {
		t.Error("超过大阈值的成功请求必须记 body_bytes")
	}
}

// TestEndpoint_FastSmallSuccessStaysQuiet —— 没超阈值就**不记**，别拿噪音淹没内存环。
func TestEndpoint_FastSmallSuccessStaysQuiet(t *testing.T) {
	srv, _ := newTestServer(t, `{"ok":1}`, false) // 默认阈值（60s / 512KB）
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()
	log := srv.declog

	resp, err := http.Post(ts.URL+"/v1/chat/completions", "application/json",
		strings.NewReader(`{"model":"m","messages":[]}`))
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	resp.Body.Close()

	rec := log.Recent(0)
	if len(rec) != 1 {
		t.Fatalf("决策日志 %d 条，want 1", len(rec))
	}
	if rec[0].ElapsedMs != 0 || rec[0].BodyBytes != 0 {
		t.Errorf("快而小的成功请求不该带 elapsed/body（会淹没 1024 条的内存环），实际 %+v", rec[0])
	}
}

// TestEndpoint_Doctor_HealthyIsOK —— 没指标 / 没失败时，/doctor 报 ok 且 **200**。
//
// 它必须是 200：dev-fleet 的 cli 探针按退出码判，而 httpassert 若把它当存活探针，
// 就会在"自愈失效"时去重启服务——那解决不了问题，还会清空现场。
func TestEndpoint_Doctor_HealthyIsOK(t *testing.T) {
	srv, _ := newTestServer(t, `{}`, false)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/doctor")
	if err != nil {
		t.Fatalf("get doctor: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Errorf("status = %d, want 200（没在失败就不该升级）", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), `"status":"ok"`) {
		t.Errorf("body = %s, want status=ok", body)
	}
}

// TestEndpoint_Doctor_EscalatesWhenSelfHealingFailed —— 连续失败超过承诺 ⇒ 503 + 理由。
//
// 【反证】把 Escalate 里的阈值比较改成恒 false ⇒ 本条变红（一个永远说 ok 的监测器）。
func TestEndpoint_Doctor_EscalatesWhenSelfHealingFailed(t *testing.T) {
	// 用**可控时钟**直接把窗口灌成"已经坏了 10 秒"，而不是真发请求再等——
	// 确定、不依赖真实时间，也不必绕 HTTP。
	// （注：DegradedSec 是整秒，刚发生的失败读作 0，所以没法用"纳秒阈值 + 立刻发一条"来测。）
	base := time.Unix(1_700_000_000, 0)
	now := base
	w := metrics.New(60, func() time.Time { return now })
	w.Observe(base, "openai/literal", false, time.Millisecond) // 这一波从 base 开始
	now = base.Add(10 * time.Second)                           // 承诺 5s，已坏 10s

	srv, _ := newTestServer(t, `{}`, false)
	srv.WithMetrics(w, 5*time.Second)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/doctor")
	if err != nil {
		t.Fatalf("get doctor: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503（自愈失效必须升级）", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), "escalate") || !strings.Contains(string(body), "reason") {
		t.Errorf("body = %s，应含 status=escalate 与 reason（人要看得懂为什么被叫醒）", body)
	}
}

// TestDecisionLog_RingBuffer —— 决策日志环形覆盖 + Recent 顺序。
func TestDecisionLog_RingBuffer(t *testing.T) {
	l := NewDecisionLog(3)
	for i := 0; i < 5; i++ {
		l.Record(DecisionEntry{ProviderID: "p", Status: 200})
	}
	if l.Len() != 3 {
		t.Errorf("len = %d, want 3 (capped)", l.Len())
	}
	rec := l.Recent(0)
	if len(rec) != 3 {
		t.Fatalf("recent = %d, want 3", len(rec))
	}
	// 满后 Recent 返回最后写入的 3 条（覆盖语义：早于容量的被淘汰）。
}

// 防 import 裁剪
var _ provider.Kind
