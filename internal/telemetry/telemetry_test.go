package telemetry

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"moretoken/internal/config"
	"moretoken/internal/router"
)

func fixedClock(t time.Time) (func() time.Time, *time.Time) {
	cur := t
	return func() time.Time { return cur }, &cur
}

// TestRecorder_WriteRequestAndSnapshot：一行请求 + 一行快照，公共字段齐全。
func TestRecorder_WriteRequestAndSnapshot(t *testing.T) {
	dir := t.TempDir()
	nowFn, _ := fixedClock(time.Date(2026, 10, 25, 12, 0, 0, 0, time.Local))
	tel := New(Options{
		Dir: dir, RetentionDays: 30, BootID: "boot01", ConfigHash: "cfg123", Now: nowFn,
	})
	defer tel.Close()

	if !tel.Enabled() {
		t.Fatal("telemetry 应启用")
	}
	tel.WriteRequest(&Request{
		RequestID: "r1", Client: "cc-mt", Protocol: "anthropic", Stream: true,
		Want: "auto:coding", Virtual: true, WantKind: "coding",
		Provider: "agnes-anthropic", Model: "agnes-2.5-flash",
		Status: 200, Outcome: "ok", TotalMs: 1234, TTFTMs: 456,
		Attempts: []router.Attempt{{
			Provider: "agnes-anthropic", Model: "agnes-2.5-flash",
			KeyID: "agnes-anthropic/key0", Kind: "ok", Status: 200, ElapsedMs: 1200,
		}},
	})
	tel.WriteMap("snapshot", map[string]any{"tier": "free", "uptime_s": 60})

	raw, err := os.ReadFile(filepath.Join(dir, "telemetry-2026-10-25.jsonl"))
	if err != nil {
		t.Fatalf("读记录: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(string(raw)), "\n")
	if len(lines) != 2 {
		t.Fatalf("行数 = %d, want 2\n%s", len(lines), raw)
	}

	var req map[string]any
	if err := json.Unmarshal([]byte(lines[0]), &req); err != nil {
		t.Fatal(err)
	}
	for k, want := range map[string]any{
		"type": "request", "boot_id": "boot01", "config_hash": "cfg123",
		"request_id": "r1", "outcome": "ok",
	} {
		if req[k] != want {
			t.Errorf("%s = %v, want %v", k, req[k], want)
		}
	}
	atts, _ := req["attempts"].([]any)
	if len(atts) != 1 {
		t.Fatalf("attempts = %v, want 1", req["attempts"])
	}
	if atts[0].(map[string]any)["key_id"] != "agnes-anthropic/key0" {
		t.Error("attempt 必须带 key_id")
	}

	var snap map[string]any
	if err := json.Unmarshal([]byte(lines[1]), &snap); err != nil {
		t.Fatal(err)
	}
	if snap["type"] != "snapshot" || snap["tier"] != "free" {
		t.Errorf("snapshot = %v", snap)
	}
}

// TestRecorder_RotatesByDayAndPrunes：跨日换文件；超过保留期的旧文件被清。
func TestRecorder_RotatesByDayAndPrunes(t *testing.T) {
	dir := t.TempDir()
	old := filepath.Join(dir, "telemetry-2026-09-01.jsonl")
	if err := os.WriteFile(old, []byte("{}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	nowFn, cur := fixedClock(time.Date(2026, 10, 25, 23, 59, 0, 0, time.Local))
	tel := New(Options{Dir: dir, RetentionDays: 30, Now: nowFn})
	defer tel.Close()

	if _, err := os.Stat(old); !os.IsNotExist(err) {
		t.Fatal("超过 30 天的旧文件应被清理")
	}
	tel.WriteMap("boot", nil)
	*cur = cur.Add(2 * time.Minute) // 跨到 10-26
	tel.WriteMap("snapshot", nil)

	for _, day := range []string{"2026-10-25", "2026-10-26"} {
		if _, err := os.Stat(filepath.Join(dir, "telemetry-"+day+".jsonl")); err != nil {
			t.Errorf("%s 应存在：%v", day, err)
		}
	}
}

// TestParseUsage：OpenAI / Anthropic / message.usage / 坏 JSON。
func TestParseUsage(t *testing.T) {
	cases := []struct {
		name string
		in   string
		ti   int
		to   int
		ok   bool
	}{
		{"openai", `{"usage":{"prompt_tokens":11,"completion_tokens":22}}`, 11, 22, true},
		{"anthropic", `{"usage":{"input_tokens":7,"output_tokens":9}}`, 7, 9, true},
		{"message", `{"message":{"usage":{"input_tokens":3,"output_tokens":4}}}`, 3, 4, true},
		{"none", `{"content":"hi"}`, 0, 0, false},
		{"bad", `{`, 0, 0, false},
	}
	for _, c := range cases {
		ti, to, ok := ParseUsage([]byte(c.in))
		if ti != c.ti || to != c.to || ok != c.ok {
			t.Errorf("%s: got (%d,%d,%v), want (%d,%d,%v)", c.name, ti, to, ok, c.ti, c.to, c.ok)
		}
	}
}

// TestConfigHash_IgnoresKeysButTracksModels：key 变不影响指纹；模型/kinds 变要变。
func TestConfigHash_IgnoresKeysButTracksModels(t *testing.T) {
	base := &config.Config{
		Listen: ":8462",
		Providers: []config.Provider{{
			ID: "p", BaseURL: "https://x", Format: "openai", Tier: "free",
			Keys:   []string{"sk-a"},
			Models: []config.Model{{ID: "m", Kinds: []string{"general"}}},
		}},
	}
	keyChanged := *base
	keyChanged.Providers = []config.Provider{{
		ID: "p", BaseURL: "https://x", Format: "openai", Tier: "free",
		Keys:   []string{"sk-b"}, // 只有 key 变
		Models: []config.Model{{ID: "m", Kinds: []string{"general"}}},
	}}
	if ConfigHash(base) != ConfigHash(&keyChanged) {
		t.Fatal("key 变化不应改变策略面指纹（记录里不能出现密钥指纹）")
	}

	modelChanged := *base
	modelChanged.Providers = []config.Provider{{
		ID: "p", BaseURL: "https://x", Format: "openai", Tier: "free",
		Keys:   []string{"sk-a"},
		Models: []config.Model{{ID: "m", Kinds: []string{"reasoning"}}},
	}}
	if ConfigHash(base) == ConfigHash(&modelChanged) {
		t.Fatal("kinds 变化必须改变指纹")
	}
}

// TestNilRecorder：nil / 未配置目录时所有方法安全。
func TestNilRecorder(t *testing.T) {
	var tel *Recorder
	tel.WriteRequest(&Request{})
	tel.WriteMap("boot", nil)
	tel.Close()
	if tel.Enabled() {
		t.Fatal("nil recorder 不应启用")
	}
	disabled := New(Options{})
	if disabled.Enabled() {
		t.Fatal("空目录应禁用")
	}
}
