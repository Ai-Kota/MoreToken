package config

import (
	"os"
	"path/filepath"
	"testing"
)

func writeTemp(t *testing.T, content string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatalf("write temp config: %v", err)
	}
	return p
}

const validConfig = `{
  "listen": ":8462",
  "providers": [
    {
      "id": "agnes",
      "base_url": "https://apihub.agnes-ai.com/v1",
      "format": "openai",
      "tier": "free",
      "keys": ["sk-a", "sk-b"],
      "models": [{"id": "agnes-2.5-flash", "name": "Agnes 2.5 Flash"}]
    },
    {
      "id": "deepseek-anthropic",
      "base_url": "https://api.deepseek.com/anthropic",
      "format": "anthropic",
      "tier": "paid",
      "keys": ["sk-pay"],
      "models": [{"id": "deepseek-v4-flash", "name": "DeepSeek V4 Flash"}]
    }
  ]
}`

func TestLoadValid(t *testing.T) {
	cfg, err := Load(writeTemp(t, validConfig))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Listen != ":8462" {
		t.Errorf("Listen = %q, want :8462", cfg.Listen)
	}
	if len(cfg.Providers) != 2 {
		t.Fatalf("providers = %d, want 2", len(cfg.Providers))
	}
	// 多 key 轮换正确解析
	if len(cfg.Providers[0].Keys) != 2 {
		t.Errorf("agnes keys = %d, want 2", len(cfg.Providers[0].Keys))
	}
	// 分层过滤
	if got := len(cfg.FreeProviders()); got != 1 {
		t.Errorf("FreeProviders = %d, want 1", got)
	}
	if got := len(cfg.PaidProviders()); got != 1 {
		t.Errorf("PaidProviders = %d, want 1", got)
	}
}

func TestLoadDefaultListen(t *testing.T) {
	cfg, err := Load(writeTemp(t, `{"providers":[{"id":"p","base_url":"http://x","format":"openai","tier":"free","keys":["k"],"models":[]}]}`))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Listen != ":8462" {
		t.Errorf("Listen default = %q, want :8462", cfg.Listen)
	}
}

func TestLoadErrors(t *testing.T) {
	cases := []struct {
		name string
		cfg  string
		want string // 期望错误子串
	}{
		{"duplicate id", `{"providers":[{"id":"p","base_url":"http://x","format":"openai","tier":"free","keys":["k"]},{"id":"p","base_url":"http://y","format":"openai","tier":"free","keys":["k"]}]}`, "duplicate provider"},
		{"bad format", `{"providers":[{"id":"p","base_url":"http://x","format":"gemini","tier":"free","keys":["k"]}]}`, "invalid format"},
		{"bad tier", `{"providers":[{"id":"p","base_url":"http://x","format":"openai","tier":"pro","keys":["k"]}]}`, "invalid tier"},
		{"no key", `{"providers":[{"id":"p","base_url":"http://x","format":"openai","tier":"free"}]}`, "at least 1 key"},
		{"no base url", `{"providers":[{"id":"p","format":"openai","tier":"free","keys":["k"]}]}`, "base_url required"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Load(writeTemp(t, tc.cfg))
			if err == nil {
				t.Fatalf("want error containing %q, got nil", tc.want)
			}
			got := err.Error()
			if !contains(got, tc.want) {
				t.Errorf("error %q does not contain %q", got, tc.want)
			}
		})
	}
}

func TestLoadMissingFile(t *testing.T) {
	if _, err := Load(filepath.Join(t.TempDir(), "nope.json")); err == nil {
		t.Fatal("want error for missing file, got nil")
	}
}

func contains(s, sub string) bool {
	return len(sub) == 0 || (len(s) >= len(sub) && indexOf(s, sub) >= 0)
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
