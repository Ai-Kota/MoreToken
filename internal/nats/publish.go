// Package nats 把网关状态/事件发布到 NATS 总线（T-005，供 WorkBoard 订阅）。
//
// 零外部依赖铁律：不引 nats.go。发布经 exec 调本机 nats.cli（AImlyForge 神经中枢既有工具），
// NATS 不可用 / CLI 缺失时静默降级 no-op——纯后端可独立运行，NATS 是增强非依赖。
// 主题命名遵循 deploy/nats/NEURAL_BACKBONE.md §四：aimly.<域>.<操作>.<目标>。
package nats

import (
	"bytes"
	"context"
	"encoding/json"
	"log"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"moretoken/internal/metrics"
)

// 主题（注册表见 NEURAL_BACKBONE.md §4.5，本任务新增 2 条 freetier）：
//   aimly.system.freetier.status —— 各池 Health + 当前 tier（定期/变化时）
//   aimly.system.freetier.event  —— state 层切换/回归/抖动事件（即时广播）
const (
	SubjectStatus = "aimly.system.freetier.status"
	SubjectEvent  = "aimly.system.freetier.event"
)

// StatusPayload 定期状态快照（/health 同构 + 当前层 + 服务质量）。密钥安全：无 key 值、无请求体。
type StatusPayload struct {
	At    time.Time `json:"at"`
	Tier  string    `json:"tier"` // "free" | "paid"
	Pools []Pool    `json:"pools"`

	// Metrics 对外服务质量（滚动窗口）。
	//
	// 为什么并进 status 而不是新开一个主题：主题是**注册制**的（NEURAL_BACKBONE §四），
	// 新主题要登记；而"我现在的状态"本来就该包含"我最近干得怎么样"。
	// 加字段对既有订阅方（WorkBoard 面板）是安全的——它只挑自己认识的字段读。
	//
	// 指标全部来自**真实请求**，零新增探测：不烧它本该保护的那份免费配额
	//（model-watchdog 前车之鉴）。
	Metrics metrics.Snapshot `json:"metrics"`
}

// Pool 一个 provider 池状态（与 router.PoolHealth 字段对齐）。
type Pool struct {
	Provider string `json:"provider"`
	Format   string `json:"format"`
	Tier     string `json:"tier"`
	// Keys = 池内真实可用 key 数；Unresolved = key 指针没取到值的条数。
	// 两者分开报，WorkBoard 才能显示"这个池是空的、因为 N 把 key 没解析出来"，
	// 而不是看着一个非零的 keys 却等不到响应。
	Keys       int    `json:"keys"`
	Unresolved int    `json:"unresolved"`
	Cooling    int    `json:"cooling"`
	BackedOff  bool   `json:"backed_off"`
}

// EventPayload 一次层切换/回归事件。Reason 见 state.Event。
type EventPayload struct {
	At     time.Time `json:"at"`
	From   string    `json:"from"` // "free" | "paid"
	To     string    `json:"to"`
	Reason string    `json:"reason"` // "all-free-unavailable" / "free-healthy>=T" / "jitter-aborted"
}

// DefaultCLIPath nats CLI 的默认绝对路径。
//
// 部署侧注入的是**连接三件套**，并没有保证 PATH 里有 nats——实测
// `E:\AImlyForge\tools\bin` 不在服务进程的 PATH 里，于是 LookPath("nats") 失败、
// 发布器静默退化成 no-op，监控前端永远等不到数据。故回落到已知绝对路径，
// 并留 NATS_BIN 覆盖（与 vault 的 VAULT_BIN 同款）。
const DefaultCLIPath = `E:\AImlyForge\tools\bin\nats.exe`

// 连接三件套的环境变量名（见 deploy/nats 的连接指南：**连接 = 环境变量三件套，唯一方式**）。
//
// ⚠️ 关键：这三个名字 **stock nats CLI 一个都不认**。CLI 认的是
//
//	AImlyForge 约定      stock nats CLI
//	NATS_SERVERS        NATS_URL
//	NATS_USER_CREDS     NATS_CREDS
//	NATS_CA_FILE        NATS_CA（且本仓用 "system" 表示"走系统信任库"）
//
// 名字不匹配的后果实测：CLI 回落到它自己的默认 context，而那个 context 指向
// `nats://127.0.0.1:4222`（本机，不是云）→ 连不上。
//
// 修法是在**调用侧**翻译成 CLI 认的 flag，而不是改部署侧造第二套变量名——
// 连接指南明写禁止"一服务一形态"。
const (
	envServers = "NATS_SERVERS"
	envCreds   = "NATS_USER_CREDS"
	envCAFile  = "NATS_CA_FILE"
)

// publishTimeout 单次发布的墙钟上限。
//
// 原为 3s，对"DNS + TLS + 鉴权"的云端往返偏紧：本机实测的失败形态是
// `lookup www.aimly.top: no such host` 与 `dial tcp ... i/o timeout`，
// 都是网络抖动而非服务端故障（同一时刻手发 nats pub 能成功）。
// 放到 8s 让正常路径有富余；周期发布器 30s 一拍仍有足够余量，
// 事件路径已改成异步（见 main.go），慢也不会拖住客户端请求。
const publishTimeout = 8 * time.Second

// publishAttempts 单条消息的尝试次数。
// 到云端的间歇性 DNS/网络抖动是本机实测常态（context-store / jiutian /
// master-loop 的日志里同样在报），重试一次就能把这类一过性抖动从
// "丢一条状态快照"降级成"晚几秒到"——这正是不想再靠事后反推现场的原因。
const publishAttempts = 2

// publishRetryGap 两次尝试之间的间隔。
const publishRetryGap = 1 * time.Second

// Publisher 经 exec nats.cli 发布。nil 安全（未初始化时 Publish 直接返回）。
type Publisher struct {
	cliPath string // "" = 不可用（降级 no-op）
	servers string
	creds   string
	caFile  string

	mu      sync.Mutex
	lastErr string // 上次失败原因：仅当变化时才记日志，避免每 30s 刷屏
}

// NewPublisher 探测 nats CLI：先 NATS_BIN，再默认绝对路径，最后 PATH。
// 都找不到 → 降级 no-op（纯后端仍可独立运行，NATS 是增强非依赖）。
func NewPublisher() *Publisher {
	bin := os.Getenv("NATS_BIN")
	if bin == "" {
		bin = DefaultCLIPath
	}
	if !executable(bin) {
		bin = "nats" // 兜底：部署侧也许真放进了 PATH
		if !executable(bin) {
			log.Printf("nats: CLI 不可用（试过 %s 与 PATH）→ 状态/事件发布降级 no-op，"+
				"监控前端将收不到数据", DefaultCLIPath)
			return &Publisher{cliPath: ""}
		}
	}
	p := &Publisher{
		cliPath: bin,
		servers: os.Getenv(envServers),
		creds:   os.Getenv(envCreds),
		caFile:  os.Getenv(envCAFile),
	}
	log.Printf("nats: 发布已启用（cli=%s server=%s）", bin, orNone(p.servers))
	return p
}

func executable(path string) bool {
	_, err := exec.LookPath(path)
	return err == nil
}

func orNone(s string) string {
	if s == "" {
		return "<未设置>"
	}
	return s
}

// Enabled 是否真能发布（false = no-op 降级中）。
func (p *Publisher) Enabled() bool { return p != nil && p.cliPath != "" }

// args 拼装 nats CLI 参数：把 AImlyForge 三件套翻译成 CLI 认的 flag。
func (p *Publisher) args(subject, body string) []string {
	a := []string{"pub", subject, body}
	if p.servers != "" {
		a = append(a, "--server", p.servers)
	}
	if p.creds != "" {
		a = append(a, "--creds", p.creds)
	}
	// "system" 是 AImlyForge 的约定值，意为"走系统信任库"——CLI 没有对应 flag
	// （实测不传也连得上），故只在它是真实文件路径时才传 --tlsca。
	if p.caFile != "" && !strings.EqualFold(p.caFile, "system") {
		a = append(a, "--tlsca", p.caFile)
	}
	return a
}

// Publish 发一条 JSON payload 到 subject。
//
// 降级语义不变：NATS 故障绝不阻塞路由主链路（调用方仍可不看返回值）。
// 但**失败不再完全静默**——按原因去重记一条 WARN。
// 之前三层断点叠加（PATH + 变量名 + context 指错）却一声不吭，
// 结果就是"监控界面永远显示总线离线、而没人知道为什么"。
func (p *Publisher) Publish(subject string, payload any) error {
	if !p.Enabled() {
		return nil
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}

	var lastErr error
	for attempt := 0; attempt < publishAttempts; attempt++ {
		if attempt > 0 {
			time.Sleep(publishRetryGap)
		}
		if lastErr = p.publishOnce(subject, string(body)); lastErr != nil {
			continue
		}
		p.clearFailure()
		return nil
	}
	return lastErr
}

// publishOnce 一次发布尝试（超时 + 失败留痕；noteFailure 按原因去重，重试不会刷屏）。
func (p *Publisher) publishOnce(subject, body string) error {
	ctx, cancel := context.WithTimeout(context.Background(), publishTimeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, p.cliPath, p.args(subject, body)...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		p.noteFailure(subject, err, stderr.String())
		return err
	}
	return nil
}

// noteFailure 记一条失败——只在**原因变化**时记，避免 30s 一条刷屏。
func (p *Publisher) noteFailure(subject string, err error, stderr string) {
	msg := strings.TrimSpace(stderr)
	if msg == "" {
		msg = err.Error()
	}
	msg = truncate(msg, 200)

	p.mu.Lock()
	changed := msg != p.lastErr
	p.lastErr = msg
	p.mu.Unlock()

	if changed {
		log.Printf("WARN nats 发布失败（subject=%s）：%s", subject, msg)
	}
}

// clearFailure 发布成功后清掉失败态，使下次失败能再记一条。
func (p *Publisher) clearFailure() {
	p.mu.Lock()
	p.lastErr = ""
	p.mu.Unlock()
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
