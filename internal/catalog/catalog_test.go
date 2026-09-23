package catalog

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"moretoken/internal/config"
)

func TestListURL(t *testing.T) {
	cases := []struct {
		base   string
		format config.Format
		want   string
	}{
		{"https://api.xkiro.com/v1", config.FormatOpenAI, "https://api.xkiro.com/v1/models"},
		{"https://apihub.agnes-ai.com", config.FormatAnthropic, "https://apihub.agnes-ai.com/v1/models"},
		{"https://apihub.agnes-ai.com/v1/", config.FormatOpenAI, "https://apihub.agnes-ai.com/v1/models"},
		// 历史写法（base 已含端点名）不重复拼接
		{"https://x/v1/models", config.FormatOpenAI, "https://x/v1/models"},
	}
	for _, c := range cases {
		if got := ListURL(c.base, c.format); got != c.want {
			t.Errorf("ListURL(%q,%s) = %q, want %q", c.base, c.format, got, c.want)
		}
	}
}

func TestIsChatCandidate(t *testing.T) {
	yes := []string{"agnes-2.5-pro", "qwen/qwen3.8-max:free", "anthropic/claude-opus-5", "gpt-5.6-terra"}
	no := []string{"agnes-image-2.0-flash", "agnes-video-2.5", "text-embedding-3", "whisper-large", "bge-rerank-v2"}
	for _, id := range yes {
		if !IsChatCandidate(id) {
			t.Errorf("%q 是对话模型，不该被筛掉", id)
		}
	}
	for _, id := range no {
		if IsChatCandidate(id) {
			t.Errorf("%q 明显不是对话模型，不该浪费一次实测", id)
		}
	}
}

// newUpstream 造一个假上游：目录列出 ids；对 good 前缀返回 200，其余按 failStatus 失败。
func newUpstream(t *testing.T, ids []string, failStatus int) (*httptest.Server, *sync.Map) {
	t.Helper()
	var calls sync.Map
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/models"):
			var b strings.Builder
			b.WriteString(`{"data":[`)
			for i, id := range ids {
				if i > 0 {
					b.WriteString(",")
				}
				b.WriteString(`{"id":"` + id + `"}`)
			}
			b.WriteString(`]}`)
			w.Write([]byte(b.String()))
		case r.Method == http.MethodPost:
			body := make([]byte, r.ContentLength)
			_, _ = r.Body.Read(body)
			m := modelOf(body)
			calls.Store(m, true)
			if strings.HasPrefix(m, "good") {
				w.Write([]byte(`{"choices":[{"message":{"content":"ok"}}]}`))
				return
			}
			w.WriteHeader(failStatus)
			w.Write([]byte(`{"error":{"message":"nope"}}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	return srv, &calls
}

func modelOf(body []byte) string {
	var v struct {
		Model string `json:"model"`
	}
	_ = json.Unmarshal(body, &v)
	return v.Model
}

// TestHarvest_VerifiesByCallingAndSkipsKnown —— 纳新的核心契约：
//   - 已在 config 里的**不重复实测**（Known）
//   - 明显非对话的**不浪费请求**
//   - 只有真调通 2xx 的才算通过（列表 ≠ 可用）
//   - 未通过的**留下原文**（下周它可能变好，理由要能回看）
func TestHarvest_VerifiesByCallingAndSkipsKnown(t *testing.T) {
	srv, calls := newUpstream(t,
		[]string{"good-a", "bad-b", "image-x", "known-c"}, http.StatusInternalServerError)

	h := Harvester{
		Client: srv.Client(),
		Provider: config.Provider{
			ID: "p", BaseURL: srv.URL + "/v1", Format: config.FormatOpenAI, Tier: config.TierFree,
		},
		APIKey: "sk-test",
		Known:  []string{"known-c"},
	}
	res, err := h.Run(context.Background())
	if err != nil {
		t.Fatalf("harvest: %v", err)
	}

	if len(res.Verified) != 1 || res.Verified[0].ID != "good-a" {
		t.Errorf("verified = %v, want [good-a]", res.Verified)
	}
	if res.Skipped != 1 {
		t.Errorf("Skipped = %d, want 1（known-c 已在库，不该重复实测）", res.Skipped)
	}
	if len(res.Rejected) != 1 || res.Rejected[0].ID != "bad-b" {
		t.Fatalf("rejected = %v, want [bad-b]", res.Rejected)
	}
	if !strings.Contains(res.Rejected[0].Err, "500") {
		t.Errorf("未通过的理由应保留上游原文（含状态码），实际 %q", res.Rejected[0].Err)
	}

	if _, ok := calls.Load("image-x"); ok {
		t.Error("image-x 明显不是对话模型，不该为它花一次实测请求")
	}
	if _, ok := calls.Load("known-c"); ok {
		t.Error("known-c 已在 config 里，不该重复实测")
	}
	if _, ok := calls.Load("bad-b"); !ok {
		t.Error("bad-b 在上游目录里，必须真调过才知道它不可用")
	}
}

// TestHarvest_NoCredentialIsError —— 没凭据就别假装纳新了。
func TestHarvest_NoCredentialIsError(t *testing.T) {
	h := Harvester{
		Client:   http.DefaultClient,
		Provider: config.Provider{ID: "p", BaseURL: "https://example.invalid/v1", Format: config.FormatOpenAI},
		APIKey:   "vault:some/pointer", // 未解析的占位符
	}
	if _, err := h.Run(context.Background()); err == nil {
		t.Fatal("占位符凭据应报错，而不是拿它去打上游换 401")
	}
}

// TestHarvest_MaxBoundsRequests —— Max 是**成本闸**：免费额度按账号限流，
// 一次纳新把整张目录打一遍比失败更糟。
func TestHarvest_MaxBoundsRequests(t *testing.T) {
	many := []string{"good-1", "good-2", "good-3", "good-4", "good-5"}
	srv, calls := newUpstream(t, many, http.StatusOK)

	h := Harvester{
		Client:   srv.Client(),
		Provider: config.Provider{ID: "p", BaseURL: srv.URL + "/v1", Format: config.FormatOpenAI},
		APIKey:   "sk-test",
		Max:      2,
	}
	res, err := h.Run(context.Background())
	if err != nil {
		t.Fatalf("harvest: %v", err)
	}
	n := 0
	calls.Range(func(_, _ any) bool { n++; return true })
	if n != 2 {
		t.Errorf("实测请求数 = %d, want 2（Max 上限）", n)
	}
	if res.Truncated != 3 {
		t.Errorf("Truncated = %d, want 3 —— 被上限挡下的必须显式计数，不能静默丢", res.Truncated)
	}
}

// TestHarvest_FreeMarkerGoesFirst —— 上游标了免费的先测。
//
// 血账（2026-09-20 实测）：xkiro 目录 111 个模型，27 个带 `:free`。
// 目录把旗舰排在前面，而旗舰对这一档凭据**全部 403**——
// Max=6 时六个候选全 403，27 个真正可用的免费模型一个都没轮到。
//
// 【反证】把 freeFirst 拿掉 ⇒ 被测的前两个变成 flagship-*（目录顺序）⇒ 本条变红。
func TestHarvest_FreeMarkerGoesFirst(t *testing.T) {
	ids := []string{"flagship-a", "flagship-b", "good-x:free", "good-y:free", "flagship-c"}
	srv, calls := newUpstream(t, ids, http.StatusOK)

	h := Harvester{
		Client:   srv.Client(),
		Provider: config.Provider{ID: "p", BaseURL: srv.URL + "/v1", Format: config.FormatOpenAI},
		APIKey:   "sk-test",
		Max:      2, // 只够测两个
	}
	if _, err := h.Run(context.Background()); err != nil {
		t.Fatalf("harvest: %v", err)
	}

	for _, want := range []string{"good-x:free", "good-y:free"} {
		if _, ok := calls.Load(want); !ok {
			t.Errorf("%s 带 :free 标记，成本闸的预算该先给它", want)
		}
	}
	if _, ok := calls.Load("flagship-a"); ok {
		t.Error("旗舰排在目录前面，但不该抢走预算 —— 实测它们对这一档凭据全是 403")
	}
}

// TestFreeFirst_IsStableAndTotalLossless —— 排序只重排，不增不减。
func TestFreeFirst_IsStableAndTotalLossless(t *testing.T) {
	in := []ModelInfo{{ID: "a"}, {ID: "f1:free"}, {ID: "b"}, {ID: "f2:free"}}
	got := freeFirst(in)

	want := []string{"f1:free", "f2:free", "a", "b"}
	if len(got) != len(want) {
		t.Fatalf("长度变了：%v", got)
	}
	for i := range want {
		if got[i].ID != want[i] {
			t.Fatalf("freeFirst = %v, want %v（免费优先 + 其余保持原序）", got, want)
		}
	}
}

// TestFetch_CarriesUpstreamMetadata —— 上游自愿给的窗口/输出上限要带回来。
//
// 它不是锦上添花：`/v1/models` 靠它对外声明 `max_input_tokens`，
// 而**声明窗口**是"客户端在超限前压缩"的前提（2026-09-22 那起卡死会话的正解）。
// 上游不给（agnes 就是这样）⇒ 留 0，**绝不猜**。
func TestFetch_CarriesUpstreamMetadata(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/models") {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		io.WriteString(w, `{"data":[
			{"id":"with-meta","context_length":1000000,"max_output_tokens":65536},
			{"id":"no-meta"}
		]}`)
	}))
	t.Cleanup(srv.Close)

	got, err := Fetch(context.Background(), srv.Client(), config.Provider{
		ID: "p", BaseURL: srv.URL + "/v1", Format: config.FormatOpenAI,
	}, "sk-test")
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	byID := map[string]ModelInfo{}
	for _, m := range got {
		byID[m.ID] = m
	}
	if m := byID["with-meta"]; m.ContextLength != 1000000 || m.MaxOutputTokens != 65536 {
		t.Errorf("with-meta = %+v，上游给的窗口/输出上限必须带回来", m)
	}
	if m := byID["no-meta"]; m.ContextLength != 0 || m.MaxOutputTokens != 0 {
		t.Errorf("no-meta = %+v，上游没给就留 0（绝不猜）", m)
	}
}
