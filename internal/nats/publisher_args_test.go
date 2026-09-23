package nats

import (
	"strings"
	"testing"
)

// TestArgs_TranslatesConventionToCLIFlags —— 本包的核心修复。
//
// AImlyForge 约定用 NATS_SERVERS / NATS_USER_CREDS / NATS_CA_FILE，
// 而 stock nats CLI 认的是 NATS_URL / NATS_CREDS / NATS_CA——名字一个都对不上。
// 不翻译的后果实测过：CLI 回落到自己的默认 context（指向 127.0.0.1:4222，本机非云）
// → 连不上 → 发布全失败。
func TestArgs_TranslatesConventionToCLIFlags(t *testing.T) {
	p := &Publisher{
		cliPath: "nats",
		servers: "nats://www.aimly.top:14222",
		creds:   "C:/creds/bridge.creds",
		caFile:  "system",
	}
	got := p.args("aimly.system.freetier.status", `{"tier":"free"}`)
	want := []string{"pub", "aimly.system.freetier.status", `{"tier":"free"}`,
		"--server", "nats://www.aimly.top:14222",
		"--creds", "C:/creds/bridge.creds"}

	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("args =\n  %v\nwant\n  %v", got, want)
	}
	// "system" 是"走系统信任库"的约定值，CLI 没有对应 flag，不该传 --tlsca
	for _, a := range got {
		if a == "--tlsca" {
			t.Error(`NATS_CA_FILE="system" 不该翻译成 --tlsca（CLI 无此语义）`)
		}
	}
}

// TestArgs_RealCAFileBecomesTlsca 真实文件路径才传 --tlsca。
func TestArgs_RealCAFileBecomesTlsca(t *testing.T) {
	p := &Publisher{cliPath: "nats", caFile: "E:/certs/ca.pem"}
	got := p.args("s", "{}")
	if !containsPair(got, "--tlsca", "E:/certs/ca.pem") {
		t.Errorf("args = %v, want 含 --tlsca E:/certs/ca.pem", got)
	}
}

// TestArgs_EmptyEnvOmitsFlags 未设置时不传空 flag（传空 flag 会让 CLI 报错）。
func TestArgs_EmptyEnvOmitsFlags(t *testing.T) {
	p := &Publisher{cliPath: "nats"}
	got := p.args("s", "{}")
	if len(got) != 3 {
		t.Errorf("args = %v, want 只有 pub/subject/body 三项", got)
	}
}

// TestNewPublisher_ResolvesDefaultPath —— nats 不在 PATH 时回落到已知绝对路径。
//
// 实测部署侧只注入了连接三件套，没保证 PATH 里有 nats；
// 旧实现 LookPath("nats") 失败即静默 no-op，监控前端永远收不到数据。
func TestNewPublisher_ResolvesDefaultPath(t *testing.T) {
	t.Setenv("NATS_BIN", DefaultCLIPath) // 指向真实存在的 nats.exe
	p := NewPublisher()
	if !p.Enabled() {
		t.Fatalf("%s 存在，发布器应启用", DefaultCLIPath)
	}
}

// TestNewPublisher_EnvOverride NATS_BIN 可覆盖（与 vault 的 VAULT_BIN 同款）。
//
// 路径刻意用**存在盘上的不存在目录**（不用 Z: 这种不存在的盘）——
// Windows 解析不存在的盘符会走超时重试，实测把这条测试从 0s 拖到 30s+。
func TestNewPublisher_EnvOverride(t *testing.T) {
	t.Setenv("NATS_BIN", "E:/__no_such_dir__/nats.exe")
	t.Setenv("PATH", "E:/__no_such_dir__") // 断掉 PATH 兜底，保证走到失败分支
	p := NewPublisher()
	if p.Enabled() {
		t.Error("NATS_BIN 指向不存在的文件、PATH 也没有 nats 时，应降级 no-op")
	}
}

// TestPublish_DisabledIsNilSafe 降级态下 Publish 恒返回 nil（NATS 是增强非依赖，
// 绝不能因为总线不可用而阻塞路由主链路）。
func TestPublish_DisabledIsNilSafe(t *testing.T) {
	p := &Publisher{cliPath: ""}
	if err := p.Publish("s", map[string]any{"a": 1}); err != nil {
		t.Errorf("降级态应返回 nil，got %v", err)
	}
	var nilP *Publisher
	if err := nilP.Publish("s", 1); err != nil {
		t.Errorf("nil Publisher 应安全，got %v", err)
	}
}

// TestNoteFailure_DedupesByReason 失败只在原因变化时记一条（30s 一次的发布不能刷屏）。
func TestNoteFailure_DedupesByReason(t *testing.T) {
	p := &Publisher{cliPath: "nats"}
	p.noteFailure("s", errString("boom"), "")
	if p.lastErr == "" {
		t.Fatal("失败原因应被记下")
	}
	first := p.lastErr
	p.noteFailure("s", errString("boom"), "") // 同一原因
	if p.lastErr != first {
		t.Error("同一原因不该改变记录")
	}
	p.clearFailure()
	if p.lastErr != "" {
		t.Error("成功后应清掉失败态，使下次失败能再记")
	}
}

type errString string

func (e errString) Error() string { return string(e) }

func containsPair(args []string, flag, val string) bool {
	for i := 0; i+1 < len(args); i++ {
		if args[i] == flag && args[i+1] == val {
			return true
		}
	}
	return false
}
