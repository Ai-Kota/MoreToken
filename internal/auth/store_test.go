package auth

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

// mockVault 记录发放面对墙的全部调用。明文经 Put 的入参可见——
// 测试断言"明文进了墙、没进 tokens.json"，真实实现里 Put 走 stdin（vault.go）。
type mockVault struct {
	mu       sync.Mutex
	puts     map[string]string
	deletes  []string
	putErr   error
	writeErr error // 模拟 tokens.json 写失败之外的墙侧失败
}

func newMockVault() *mockVault {
	return &mockVault{puts: make(map[string]string)}
}

func (m *mockVault) Put(path, plain string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.putErr != nil {
		return m.putErr
	}
	m.puts[path] = plain
	return nil
}

func (m *mockVault) Delete(path string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.deletes = append(m.deletes, path)
	return m.writeErr
}

// fakeClock 可控时钟（过期/时序格的注入手段，对齐 TEST-MATRIX §0）。
type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

func mustIssue(t *testing.T, path, name, vaultPath string, ttl time.Duration, vw VaultWriter, now time.Time) *Entry {
	t.Helper()
	e, err := Issue(path, name, vaultPath, ttl, vw, now)
	if err != nil {
		t.Fatalf("Issue(%s): %v", name, err)
	}
	return e
}

func storePath(t *testing.T) string {
	t.Helper()
	return filepath.Join(t.TempDir(), "tokens.json")
}

// ---- Verify ----

// TestVerify_HappyAndReject 正确 token 过、错 token 拒、空串拒（矩阵 正常/错误 格）。
func TestVerify_HappyAndReject(t *testing.T) {
	path := storePath(t)
	vw := newMockVault()
	now := time.Date(2026, 9, 29, 8, 0, 0, 0, time.UTC)
	mustIssue(t, path, "agent-a", "moretoken/tokens/agent-a", DefaultTTL, vw, now)
	plain := vw.puts["moretoken/tokens/agent-a"]

	s, err := LoadStore(path)
	if err != nil {
		t.Fatalf("LoadStore: %v", err)
	}
	if name, ok := s.Verify(plain); !ok || name != "agent-a" {
		t.Fatalf("Verify(正确 token) = (%q,%v), want (agent-a,true)", name, ok)
	}
	if name, ok := s.Verify("mt_totally-wrong-token"); ok || name != "" {
		t.Fatalf("Verify(错 token) = (%q,%v), want (,false)", name, ok)
	}
	if _, ok := s.Verify(""); ok {
		t.Fatal("Verify(空串) 应拒（已配置鉴权）")
	}
	// 前缀相同、后缀不同 → 拒（矩阵"边界"格）
	if _, ok := s.Verify(plain[:len(plain)-1] + "X"); ok {
		t.Fatal("仅差最后一个字符也应拒")
	}
}

// TestVerify_EmptyStoreCompat 空 Store 恒通过（兼容模式，矩阵"空"格）：
// 未配置任何 token 时行为与 T-024 之前完全一致。
func TestVerify_EmptyStoreCompat(t *testing.T) {
	s, err := LoadStore(filepath.Join(t.TempDir(), "nonexistent.json"))
	if err != nil {
		t.Fatalf("文件不存在不应报错: %v", err)
	}
	for _, tok := range []string{"", "whatever", "mt_x"} {
		if name, ok := s.Verify(tok); !ok || name != "" {
			t.Fatalf("空 Store Verify(%q) = (%q,%v), want (,true)", tok, name, ok)
		}
	}
}

// TestVerify_Expired 过期拒 + 到期瞬间即失效（now >= expires 判死，矩阵 错误/时序 格）。
// 明文从 mock vault 的 Put 入参取（发放面唯一可见明文的出口）。
func TestVerify_Expired(t *testing.T) {
	path := storePath(t)
	vw := newMockVault()
	clk := &fakeClock{t: time.Date(2026, 9, 29, 8, 0, 0, 0, time.UTC)}
	mustIssue(t, path, "short", "moretoken/tokens/short", time.Hour, vw, clk.Now())
	plain := vw.puts["moretoken/tokens/short"]

	s, err := LoadStore(path, WithClock(clk.Now))
	if err != nil {
		t.Fatalf("LoadStore: %v", err)
	}
	if _, ok := s.Verify(plain); !ok {
		t.Fatal("未过期应通过")
	}
	clk.Advance(59 * time.Minute)
	if _, ok := s.Verify(plain); !ok {
		t.Fatal("59 分钟时应仍通过")
	}
	clk.Advance(2 * time.Minute) // now == expires 恰好到期瞬间
	if name, ok := s.Verify(plain); ok {
		t.Fatalf("到期瞬间应拒（now >= expires 判死）, got (%q,true)", name)
	}
}

// TestVerify_Revoked 吊销后拒，且返回 name 供留痕（矩阵"吊销/正常"格）。
func TestVerify_Revoked(t *testing.T) {
	path := storePath(t)
	vw := newMockVault()
	now := time.Now()
	mustIssue(t, path, "leaky", "moretoken/tokens/leaky", DefaultTTL, vw, now)
	plain := vw.puts["moretoken/tokens/leaky"]

	s, _ := LoadStore(path)
	if _, ok := s.Verify(plain); !ok {
		t.Fatal("吊销前应通过")
	}
	if err := Revoke(path, "leaky", now); err != nil {
		t.Fatalf("Revoke: %v", err)
	}
	s.ReloadIfChanged() // 模拟网关下一请求的热加载
	if name, ok := s.Verify(plain); ok || name != "leaky" {
		t.Fatalf("吊销后 = (%q,%v), want (leaky,false)", name, ok)
	}
}

// TestVerify_ReissueSameNameAfterRevoke 吊销后同名重发 = 独立条目（矩阵"边界"格）。
func TestVerify_ReissueSameNameAfterRevoke(t *testing.T) {
	path := storePath(t)
	vw := newMockVault()
	now := time.Now()
	mustIssue(t, path, "agent", "moretoken/tokens/agent", DefaultTTL, vw, now)
	old := vw.puts["moretoken/tokens/agent"]

	if err := Revoke(path, "agent", now); err != nil {
		t.Fatalf("Revoke: %v", err)
	}
	mustIssue(t, path, "agent", "moretoken/tokens/agent", DefaultTTL, vw, now.Add(time.Second))
	fresh := vw.puts["moretoken/tokens/agent"] // mock 覆盖同 path，取到新明文
	if fresh == old {
		t.Fatal("重发应生成新明文")
	}
	s, _ := LoadStore(path)
	if _, ok := s.Verify(old); ok {
		t.Fatal("旧 token 应已失效")
	}
	if name, ok := s.Verify(fresh); !ok || name != "agent" {
		t.Fatalf("新 token = (%q,%v), want (agent,true)", name, ok)
	}
}

// TestIssue_DuplicateActiveName 同名 active 重复发放 → 拒（审计身份唯一性）。
func TestIssue_DuplicateActiveName(t *testing.T) {
	path := storePath(t)
	vw := newMockVault()
	now := time.Now()
	mustIssue(t, path, "dup", "moretoken/tokens/dup", DefaultTTL, vw, now)
	if _, err := Issue(path, "dup", "moretoken/tokens/dup", DefaultTTL, vw, now); err == nil {
		t.Fatal("同名 active 重复发放应报错")
	}
}

// TestIssue_VaultPutFails Vault 写失败 → tokens.json 不落盘（发放面失败必须干净）。
func TestIssue_VaultPutFails(t *testing.T) {
	path := storePath(t)
	vw := newMockVault()
	vw.putErr = errors.New("wall is down")
	if _, err := Issue(path, "x", "moretoken/tokens/x", DefaultTTL, vw, time.Now()); err == nil {
		t.Fatal("vault 失败应报错")
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("vault 失败后不得留下 tokens.json")
	}
}

// TestIssue_StoreWriteFails_RollsBackVault tokens.json 写失败 → vault delete 回滚（事务格）。
func TestIssue_StoreWriteFails_RollsBackVault(t *testing.T) {
	if runtime.GOOS != "windows" {
		// POSIX 下 rename 覆盖只读文件会成功（权限查在目录上），此手法仅 Windows 有效。
		t.Skip("rollback via read-only target is Windows-specific")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "tokens.json")
	// 先造一个合法文件再置只读：readTokenFileOrEmpty 能过，writeTokenFile 的 rename 撞只读目标失败。
	if err := os.WriteFile(path, []byte(`{"tokens":[]}`), 0o444); err != nil {
		t.Fatal(err)
	}
	vw := newMockVault()
	_, err := Issue(path, "rb", "moretoken/tokens/rb", DefaultTTL, vw, time.Now())
	if err == nil {
		t.Fatal("只读 tokens.json 上发放应失败")
	}
	if len(vw.deletes) != 1 || vw.deletes[0] != "moretoken/tokens/rb" {
		t.Fatalf("应回滚 vault delete, got %v", vw.deletes)
	}
}

// TestIssue_NoPlaintextInFile 明文永不落盘：tokens.json 里没有明文、没有可反推字段（TH2/TH3）。
func TestIssue_NoPlaintextInFile(t *testing.T) {
	path := storePath(t)
	vw := newMockVault()
	mustIssue(t, path, "secret", "moretoken/tokens/secret", DefaultTTL, vw, time.Now())
	plain := vw.puts["moretoken/tokens/secret"]

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if contains(string(raw), plain) {
		t.Fatal("tokens.json 含明文！")
	}
	if contains(string(raw), plain[len(TokenPrefix):]) {
		t.Fatal("tokens.json 含明文正文！")
	}
	var tf tokenFile
	if err := json.Unmarshal(raw, &tf); err != nil {
		t.Fatal(err)
	}
	e := tf.Tokens[0]
	if e.Hash != Hash(plain) {
		t.Fatal("hash 与明文不匹配")
	}
	if e.Prefix != DisplayPrefix(plain) {
		t.Fatalf("prefix = %q, want %q", e.Prefix, DisplayPrefix(plain))
	}
	if e.VaultPath != "moretoken/tokens/secret" {
		t.Fatalf("vault_path = %q", e.VaultPath)
	}
	if e.Expires == nil {
		t.Fatal("默认 TTL 应有 expires")
	}
}

// TestIssue_ZeroTTLNoExpiry ttl=0 → 永不过期（显式选择，矩阵"空"格）。
func TestIssue_ZeroTTLNoExpiry(t *testing.T) {
	path := storePath(t)
	e := mustIssue(t, path, "forever", "", 0, nil, time.Now())
	if e.Expires != nil {
		t.Fatalf("ttl=0 应无 expires, got %v", e.Expires)
	}
}

// TestRevoke_UnknownName 吊不存在的 name → 报错（CLI 可见性）。
func TestRevoke_UnknownName(t *testing.T) {
	path := storePath(t)
	mustIssue(t, path, "real", "", 0, nil, time.Now())
	if err := Revoke(path, "ghost", time.Now()); err == nil {
		t.Fatal("吊销不存在的 name 应报错")
	}
	if err := Revoke(filepath.Join(t.TempDir(), "none.json"), "x", time.Now()); err == nil {
		t.Fatal("文件不存在应报错")
	}
}

// TestRotate 轮换：旧 revoked + 新 active + vault path 不变（指针红利格）。
func TestRotate(t *testing.T) {
	path := storePath(t)
	vw := newMockVault()
	now := time.Now()
	mustIssue(t, path, "agent", "moretoken/tokens/agent", DefaultTTL, vw, now)
	old := vw.puts["moretoken/tokens/agent"]

	ne, err := Rotate(path, "agent", DefaultTTL, vw, now.Add(time.Second))
	if err != nil {
		t.Fatalf("Rotate: %v", err)
	}
	fresh := vw.puts["moretoken/tokens/agent"]
	if fresh == old {
		t.Fatal("轮换应产生新明文")
	}
	if ne.VaultPath != "moretoken/tokens/agent" {
		t.Fatalf("vault 指针应不变, got %q", ne.VaultPath)
	}
	s, _ := LoadStore(path)
	if _, ok := s.Verify(old); ok {
		t.Fatal("旧 token 轮换后应失效")
	}
	if name, ok := s.Verify(fresh); !ok || name != "agent" {
		t.Fatalf("新 token = (%q,%v), want (agent,true)", name, ok)
	}
	// 再轮不存在的 name → 报错
	if _, err := Rotate(path, "ghost", DefaultTTL, vw, now); err == nil {
		t.Fatal("轮换不存在的 name 应报错")
	}
}

// ---- 热加载 ----

// TestReloadIfChanged 改文件 → 下一 Verify 生效；mtime 不变不重读（矩阵"热加载"格）。
func TestReloadIfChanged(t *testing.T) {
	path := storePath(t)
	vw := newMockVault()
	now := time.Now()
	mustIssue(t, path, "a1", "moretoken/tokens/a1", DefaultTTL, vw, now)
	plain := vw.puts["moretoken/tokens/a1"]

	s, _ := LoadStore(path)
	if _, ok := s.Verify(plain); !ok {
		t.Fatal("初始应通过")
	}
	// 追加一个新 token（模拟另一 CLI 进程发放）
	e2 := mustIssue(t, path, "a2", "moretoken/tokens/a2", DefaultTTL, vw, now)
	_ = e2
	plain2 := vw.puts["moretoken/tokens/a2"]
	s.ReloadIfChanged()
	if name, ok := s.Verify(plain2); !ok || name != "a2" {
		t.Fatalf("热加载后新 token = (%q,%v), want (a2,true)", name, ok)
	}
}

// TestReloadIfChanged_BadFileKeepsOld 坏 JSON → 保旧快照 + 不 panic（"坏文件不得打瞎网关"）。
func TestReloadIfChanged_BadFileKeepsOld(t *testing.T) {
	path := storePath(t)
	vw := newMockVault()
	now := time.Now()
	mustIssue(t, path, "keep", "moretoken/tokens/keep", DefaultTTL, vw, now)
	plain := vw.puts["moretoken/tokens/keep"]

	var logged []string
	s, _ := LoadStore(path, WithLogger(func(f string, args ...any) {
		logged = append(logged, f)
	}))
	// 写坏文件（mtime 必然变化）
	time.Sleep(10 * time.Millisecond)
	if err := os.WriteFile(path, []byte(`{"tokens": [BROKEN`), 0o644); err != nil {
		t.Fatal(err)
	}
	s.ReloadIfChanged()
	if name, ok := s.Verify(plain); !ok || name != "keep" {
		t.Fatalf("坏文件后应保旧快照, got (%q,%v)", name, ok)
	}
	if len(logged) == 0 {
		t.Fatal("坏文件应留痕（日志）")
	}
	// 修好文件 → 恢复加载
	tf := tokenFile{Tokens: []Entry{{
		Name: "fixed", Hash: Hash("mt_fixed-token"), Created: now,
	}}}
	raw, _ := json.Marshal(tf)
	time.Sleep(10 * time.Millisecond)
	if err := os.WriteFile(path, raw, 0o644); err != nil {
		t.Fatal(err)
	}
	s.ReloadIfChanged()
	if _, ok := s.Verify("mt_fixed-token"); !ok {
		t.Fatal("修复后应加载新文件")
	}
}

// TestReloadIfChanged_DeletedFileKeepsOld 文件被删 → 保留旧快照（消失≠撤销鉴权）。
func TestReloadIfChanged_DeletedFileKeepsOld(t *testing.T) {
	path := storePath(t)
	vw := newMockVault()
	mustIssue(t, path, "survive", "moretoken/tokens/survive", DefaultTTL, vw, time.Now())
	plain := vw.puts["moretoken/tokens/survive"]
	s, _ := LoadStore(path)
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	s.ReloadIfChanged()
	if _, ok := s.Verify(plain); !ok {
		t.Fatal("文件被删不应打瞎在线网关（保旧快照）")
	}
}

// ---- last_used 回写 ----

// TestTouchAsync_WritesLastUsed 请求后回写 last_used（矩阵"last_used/正常"格）。
func TestTouchAsync_WritesLastUsed(t *testing.T) {
	path := storePath(t)
	mustIssue(t, path, "touchy", "", 0, nil, time.Now())
	s, _ := LoadStore(path)
	s.TouchAsync("touchy")

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		entries, err := List(path)
		if err == nil && len(entries) == 1 && entries[0].LastUsed != nil {
			return // 回写落地
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("last_used 未回写")
}

// TestTouchAsync_FailureNeverPanics 回写失败（文件被删）只记日志、不崩（"审计不得反噬可用性"）。
func TestTouchAsync_FailureNeverPanics(t *testing.T) {
	path := storePath(t)
	mustIssue(t, path, "gone", "", 0, nil, time.Now())
	s, _ := LoadStore(path)
	os.Remove(path)
	s.TouchAsync("gone") // 不得 panic
	time.Sleep(100 * time.Millisecond)
	s.TouchAsync("") // 空 name 直接忽略
}

// TestTouchAsync_Disabled touchDisabled=true → 不回写（T-026 容器 ro 挂载形态）。
// 决定性断言：请求后文件里 last_used 仍为 null，且不因 EROFS 报错/panic。
func TestTouchAsync_Disabled(t *testing.T) {
	path := storePath(t)
	mustIssue(t, path, "notouch", "", 0, nil, time.Now())
	s, _ := LoadStore(path, WithTouchDisabled(true))
	s.TouchAsync("notouch")
	time.Sleep(150 * time.Millisecond) // 给"若会写"留足时间
	entries, err := List(path)
	if err != nil || len(entries) != 1 {
		t.Fatalf("List: %v %v", entries, err)
	}
	if entries[0].LastUsed != nil {
		t.Fatal("touchDisabled 下 last_used 不应被回写")
	}
}

// TestTouchAsync_InMemoryNoop InMemoryStore（无 path）Touch 是 no-op。
func TestTouchAsync_InMemoryNoop(t *testing.T) {
	s := InMemoryStore(nil)
	s.TouchAsync("x")   // 不得 panic
	s.ReloadIfChanged() // 无 path → 直接返回
	if _, ok := s.Verify("anything"); !ok {
		t.Fatal("空内存 Store 应为兼容模式")
	}
}

// TestEntry_StatusBoundary active/expired/revoked 三态与到期瞬间边界。
func TestEntry_StatusBoundary(t *testing.T) {
	now := time.Date(2026, 9, 29, 8, 0, 0, 0, time.UTC)
	exp := now.Add(time.Hour)
	e := Entry{Name: "x", Expires: &exp}
	if e.Status(now) != "active" {
		t.Fatalf("未到期 = %s", e.Status(now))
	}
	if e.Status(exp) != "expired" {
		t.Fatal("恰好到期瞬间应判 expired（now >= expires）")
	}
	e.Revoked = true
	if e.Status(exp.Add(time.Hour)) != "revoked" {
		t.Fatal("revoked 优先于 expired")
	}
	noExp := Entry{Name: "y"}
	if noExp.Status(now.Add(100 * 365 * 24 * time.Hour)) != "active" {
		t.Fatal("无 expires = 永不过期")
	}
}

func contains(haystack, needle string) bool {
	return strings.Contains(haystack, needle)
}
