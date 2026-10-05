package proxy

import (
	"strings"
	"testing"

	"moretoken/internal/provider"
	"moretoken/internal/router"
)

// TestRouterEntry_PassthroughKeepsUpstreamBody —— 透传路径必须留上游原文（T-031）。
//
// 起因：2026-10-05 排查 cc-ft 的 404 时，`/decisions` 里那三条 `status=404` 的 `reason`
// 全空——上游说了什么、是不是"模型不存在"，一个字都没留下，只能靠推断
// （正是本仓原则「吸收即销毁证据」要堵的形态）。
//
// 机制：透传路径 `Fail.Kind==FailNone` ⇒ `Fail.Reason` 为空，但 `res.Body` 就是上游原文。
// 契约：非 2xx 且无 Reason 时，`Reason` 必须带上（截断的）上游 body。
//
// 【反证】删掉 routerEntry 里补 Reason 的那段分支 ⇒ 本条变红。
func TestRouterEntry_PassthroughKeepsUpstreamBody(t *testing.T) {
	body := `{"type":"error","error":{"type":"not_found_error","message":"model: minimax/minimax-m3:free"}}`
	res := router.Result{
		Result:     provider.Result{StatusCode: 404, Body: []byte(body)},
		ProviderID: "xkiro-anthropic",
		Retried:    true,
	}

	e := routerEntry(res)

	if e.Status != 404 {
		t.Fatalf("status = %d, want 404", e.Status)
	}
	if e.Reason == "" {
		t.Fatal("透传路径的 reason 为空 —— 现场证据在留痕里是空白（正是本任务要堵的）")
	}
	if !strings.Contains(e.Reason, "not_found_error") {
		t.Errorf("reason 未带上游原文：%q", e.Reason)
	}
	if !strings.Contains(e.Reason, "minimax") {
		t.Errorf("reason 未带上游说的模型名：%q", e.Reason)
	}
}

// TestRouterEntry_ClassifiedFailureKeepsOwnReason —— 已归类的失败**不被上游原文顶掉**。
//
// FailModelNotFound / FailUpstream 等已带自己的 Reason（含翻译后的结论，如超限的两个数），
// 它比裸上游原文更有用，不得被本段覆盖。
func TestRouterEntry_ClassifiedFailureKeepsOwnReason(t *testing.T) {
	res := router.Result{
		Result:     provider.Result{StatusCode: 503, Body: []byte("upstream noise")},
		ProviderID: "agnes-anthropic",
		Fail:       provider.Fail{Kind: provider.FailUpstream, Reason: "status 503: 上游过载"},
	}
	e := routerEntry(res)
	if e.Reason != "status 503: 上游过载" {
		t.Errorf("reason = %q —— 已归类的失败不该被上游原文顶掉", e.Reason)
	}
}
