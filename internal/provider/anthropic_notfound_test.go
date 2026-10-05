package provider

import "testing"

// TestIsModelNotFound_AnthropicCanonicalShape —— T-032 的核心契约。
//
// Anthropic 规范形状是**结构化**的（`error.type`），不是散文。识别它，
// xkiro-anthropic 对已退役模型回的那道 404 才会归 `FailModelNotFound`，
// 从而在 tryCandidate"无下一个模型"处返回 `done=false` ⇒ 沿候选链继续，
// 而不是就地终止把 404 泄漏给客户端（T-031 钉住的缺陷）。
func TestIsModelNotFound_AnthropicCanonicalShape(t *testing.T) {
	body := []byte(`{"type":"error","error":{"type":"not_found_error","message":"model: minimax/minimax-m3:free"}}`)
	if !isModelNotFound(body) {
		t.Error("Anthropic 规范 not_found_error 未被识别 —— 虚拟模型会就地终止、把 404 泄漏给客户端")
	}
}

// TestIsModelNotFound_MarkersStillWork —— 既有 OpenAI 味 marker 判据不回退。
func TestIsModelNotFound_MarkersStillWork(t *testing.T) {
	for _, b := range []string{
		`{"error":{"message":"Model \"x\" does not exist"}}`,
		`{"error":"model_not_found"}`,
		`{"error":"unknown model"}`,
		`{"error":"no such model"}`,
	} {
		if !isModelNotFound([]byte(b)) {
			t.Errorf("既有 marker 判据回退了：%s", b)
		}
	}
}

// TestIsModelNotFound_UnrelatedBodyNotMatched —— **宁可窄**：无关错误体不得被误判。
//
// 误判代价不对称：把"上游故障"误判成"模型不存在"，会让本该重试的失败变成终局。
//
// 【反证】若把判据写成裸匹配子串（如 `not_found` 或 `model`），本节会红。
func TestIsModelNotFound_UnrelatedBodyNotMatched(t *testing.T) {
	for _, b := range []string{
		``,
		`{"error":{"type":"invalid_request_error","message":"max_tokens is required"}}`,
		`{"error":{"type":"permission_error","message":"no access"}}`,
		`{"detail":"upstream unavailable"}`,
		`not json at all`,
	} {
		if isModelNotFound([]byte(b)) {
			t.Errorf("无关错误体被误判成「模型不存在」：%q", b)
		}
	}
}
