package nats

import (
	"encoding/json"
	"testing"
	"time"
)

// TestPublish_DisabledNoOp —— nats.cli 缺失（或未初始化）时 Publish 静默 no-op，
// 不 panic、不影响路由主链路（降级语义）。
func TestPublish_DisabledNoOp(t *testing.T) {
	// 未初始化（nil 接收者）→ Enabled=false → Publish 直接 nil。
	var p *Publisher
	if p.Enabled() {
		t.Error("nil publisher should be disabled")
	}
	if err := p.Publish(SubjectStatus, StatusPayload{}); err != nil {
		t.Errorf("nil publisher publish = %v, want nil (no-op)", err)
	}

	// cliPath 为空（CLI 缺失）→ 同样 no-op。
	empty := &Publisher{}
	if err := empty.Publish(SubjectEvent, EventPayload{At: time.Now()}); err != nil {
		t.Errorf("empty-cli publisher publish = %v, want nil (no-op)", err)
	}
}

// TestPublisher_DisabledNoOp —— 显式构造不可用发布器，确认两条主题都安全。
func TestPublisher_DisabledNoOp(t *testing.T) {
	p := &Publisher{}
	for _, subj := range []string{SubjectStatus, SubjectEvent} {
		if err := p.Publish(subj, StatusPayload{Tier: "free"}); err != nil {
			t.Errorf("publish %s = %v, want nil", subj, err)
		}
	}
}

// TestPayloadJSON —— payload 序列化契约（WorkBoard 订阅方据此解析）：
// 字段名稳定 + `>` 字符在 JSON 语义下无损（Go 默认转义为 >，解码后还原）。
func TestPayloadJSON(t *testing.T) {
	p := StatusPayload{
		At:   time.Unix(0, 0),
		Tier: "paid",
		Pools: []Pool{{
			Provider: "agnes", Format: "openai", Tier: "free",
			Keys: 3, Cooling: 1, BackedOff: true,
		}},
	}
	b, err := encodeJSON(p)
	if err != nil {
		t.Fatalf("encode status: %v", err)
	}
	var got StatusPayload
	if err := json.Unmarshal([]byte(b), &got); err != nil {
		t.Fatalf("round-trip status: %v", err)
	}
	if got.Tier != "paid" || len(got.Pools) != 1 || got.Pools[0].Provider != "agnes" || got.Pools[0].BackedOff != true {
		t.Errorf("status round-trip mismatch: %+v", got)
	}

	e := EventPayload{At: time.Unix(0, 0), From: "paid", To: "free", Reason: "free-healthy>=T"}
	eb, err := encodeJSON(e)
	if err != nil {
		t.Fatalf("encode event: %v", err)
	}
	var gotE EventPayload
	if err := json.Unmarshal([]byte(eb), &gotE); err != nil {
		t.Fatalf("round-trip event: %v", err)
	}
	// `>` 被转义为 >，解码后必须无损还原为原 reason。
	if gotE.Reason != "free-healthy>=T" {
		t.Errorf("event reason round-trip = %q, want %q", gotE.Reason, "free-healthy>=T")
	}
	if gotE.From != "paid" || gotE.To != "free" {
		t.Errorf("event from/to = %q/%q, want paid/free", gotE.From, gotE.To)
	}
}

// encodeJSON 测试用封装（直接 json.Marshal）。
func encodeJSON(v any) (string, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return "", err
	}
	return string(b), nil
}
