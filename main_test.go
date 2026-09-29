package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"moretoken/internal/config"
)

// newTestServer 起一个 httptest 服务（doctor 客户端用例的假网关），测试结束自动关闭。
func newTestServer(t *testing.T, h http.HandlerFunc) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return srv
}

// writeTestConfig 落一个最小合法 config（validate 要求：id/base_url/format/tier/≥1 key）。
// 用 env: 指针而非 vault:——测试不依赖 vault.exe（发放面/解析面已各有归属测试）。
func writeTestConfig(t *testing.T, dir string, keys ...string) string {
	t.Helper()
	keyJSON := make([]string, len(keys))
	for i, k := range keys {
		b, _ := json.Marshal(k)
		keyJSON[i] = string(b)
	}
	content := `{
  "listen": ":8462",
  "providers": [
    {
      "id": "test-free",
      "base_url": "https://api.example.com/v1",
      "format": "openai",
      "tier": "free",
      "keys": [` + strings.Join(keyJSON, ",") + `],
      "models": [{"id": "m1", "name": "M1", "kinds": ["general"], "context_length": 1000}]
    }
  ]
}`
	path := filepath.Join(dir, "config.json")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// TestMaterialize_RoundTrip 解析 env: 指针 → 输出完整 config → 重新 Load 等价可用；
// 输出物内**无任何指针残留**（矩阵"materialize/正常"格）。
func TestMaterialize_RoundTrip(t *testing.T) {
	dir := t.TempDir()
	cfgPath := writeTestConfig(t, dir, "env:MT_TEST_KEY_A", "env:MT_TEST_KEY_B")
	t.Setenv("MT_TEST_KEY_A", "sk-real-a")
	t.Setenv("MT_TEST_KEY_B", "sk-real-b")

	out := filepath.Join(dir, "materialized.json")
	if code := runMaterializeConfig(cfgPath, out); code != 0 {
		t.Fatalf("exit = %d, want 0", code)
	}

	raw, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "env:") || strings.Contains(string(raw), "vault:") {
		t.Fatal("输出物不得残留指针（容器里没有解析器）")
	}
	if !strings.Contains(string(raw), "sk-real-a") {
		t.Fatal("输出物应含解析后的明文 key（这是它的职责）")
	}
	// 权限 0600（明文文件的基本卫生）——仅 POSIX 有意义：Windows 用 ACL 不用 mode 位，
	// os.WriteFile(0600) 在 Windows 上一律报告 0666，断言它是在测 OS 不是测我们。
	// 容器形态的 0600 由 one-shot 的 `umask 077` 保证（mt-deploy.sh），不经这条 WriteFile。
	if runtime.GOOS != "windows" {
		fi, _ := os.Stat(out)
		if fi.Mode().Perm()&0o077 != 0 {
			t.Fatalf("权限 = %v, want 0600", fi.Mode().Perm())
		}
	}
	// round-trip：输出物是**独立可用**的 config
	cfg2, err := config.Load(out)
	if err != nil {
		t.Fatalf("round-trip Load: %v", err)
	}
	if cfg2.Providers[0].Keys[0] != "sk-real-a" || cfg2.Providers[0].Keys[1] != "sk-real-b" {
		t.Fatalf("round-trip keys 不对: %v", cfg2.Providers[0].Keys)
	}
}

// TestMaterialize_UnresolvedBlocks env 未设置 → exit 1 且**不输出半成品**（部署闸格）。
func TestMaterialize_UnresolvedBlocks(t *testing.T) {
	dir := t.TempDir()
	cfgPath := writeTestConfig(t, dir, "env:MT_TEST_KEY_MISSING")
	t.Setenv("MT_TEST_KEY_MISSING", "") // 显式空 = 未解析（ResolveKeys 口径：trim 后非空才算）

	out := filepath.Join(dir, "should-not-exist.json")
	if code := runMaterializeConfig(cfgPath, out); code != 1 {
		t.Fatalf("exit = %d, want 1（残缺 config 不许上船）", code)
	}
	if _, err := os.Stat(out); !os.IsNotExist(err) {
		t.Fatal("闸失败时不得留下输出文件")
	}
}

// TestMaterialize_StdoutClean stdout 模式：fd1 只有纯净 JSON，诊断全在 stderr。
// 管道对端（docker run -i）拿到的必须是能直接落盘的完整 config。
func TestMaterialize_StdoutClean(t *testing.T) {
	dir := t.TempDir()
	cfgPath := writeTestConfig(t, dir, "env:MT_TEST_KEY_C")
	t.Setenv("MT_TEST_KEY_C", "sk-real-c")

	// 换掉 os.Stdout 捕获写入（runMaterializeConfig 直写 os.Stdout）
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	origStdout := os.Stdout
	os.Stdout = w
	code := runMaterializeConfig(cfgPath, "-")
	os.Stdout = origStdout
	w.Close()
	if code != 0 {
		t.Fatalf("exit = %d, want 0", code)
	}
	buf := make([]byte, 1<<20)
	n, _ := r.Read(buf)
	r.Close()

	var got map[string]any
	if err := json.Unmarshal(buf[:n], &got); err != nil {
		t.Fatalf("stdout 不是纯净 JSON（诊断混进来了？）: %v\n%s", err, string(buf[:n]))
	}
	provs, _ := got["providers"].([]any)
	if len(provs) != 1 {
		t.Fatalf("providers = %d, want 1", len(provs))
	}
	if !strings.Contains(string(buf[:n]), "sk-real-c") {
		t.Fatal("stdout 应含解析后的 key")
	}
}

// TestMaterialize_BadConfigPath config 不存在 → exit 1（响亮失败）。
func TestMaterialize_BadConfigPath(t *testing.T) {
	if code := runMaterializeConfig(filepath.Join(t.TempDir(), "nope.json"), "-"); code != 1 {
		t.Fatalf("exit = %d, want 1", code)
	}
}

// TestDoctorClient_Non200NotGreen /doctor 收到 401（鉴权挡住）→ exit 2，绝不回落 ok。
// 评审 S7+代码改动 4 的决定性用例：假绿比空白更贵（T-016）。
func TestDoctorClient_Non200NotGreen(t *testing.T) {
	srv := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(401)
		w.Write([]byte(`{"error":{"message":"auth: no token presented"}}`))
	})
	if code := runDoctorClient(srv.URL + "/doctor"); code != 2 {
		t.Fatalf("401 → exit = %d, want 2（不可达/配置错档，不是 ok 也不是升级）", code)
	}
}

// TestDoctorClient_EscalateVsOK 200=ok(0)、503/escalate=升级(1) 的既有语义不被破坏。
func TestDoctorClient_EscalateVsOK(t *testing.T) {
	srv := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("mode") == "bad" {
			w.WriteHeader(503)
			w.Write([]byte(`{"status":"escalate","reason":"free tier down 200s"}`))
			return
		}
		w.WriteHeader(200)
		w.Write([]byte(`{"status":"ok","metrics":null}`))
	})
	if code := runDoctorClient(srv.URL + "/doctor"); code != 0 {
		t.Fatalf("200 ok → exit = %d, want 0", code)
	}
	if code := runDoctorClient(srv.URL + "/doctor?mode=bad"); code != 1 {
		t.Fatalf("503 escalate → exit = %d, want 1", code)
	}
}
