// Package telemetry 写一份**给智能体看的运行记录**。
//
// 设计约束（2026-10-25 用户定性）：
//   - 不要看板/告警/Prometheus；只要一份完整、可查询、重启不丢的记录；
//   - 记录给智能体做优化证据，不要求人每天盯；
//   - 不记录 key 明文、prompt、请求/响应正文；key 只记池内 ID（provider/keyN）。
//
// 形态：按天轮转的 JSONL。每个聊天请求一行 `type=request`；每 60 秒一行
// `type=snapshot`（池/tier/60 分钟指标/admission/版本），保证没有流量时也有事实。
// 目录不可写时**静默降级**（只告警一次），绝不拖垮路由主链路。
package telemetry

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"moretoken/internal/config"
	"moretoken/internal/router"
)

// Request 一条请求记录。Attempts 直接复用 router.Attempt（含 key_id / elapsed / stream）。
type Request struct {
	Type      string `json:"type"`
	TS        string `json:"ts"`
	RequestID string `json:"request_id"`
	BootID    string `json:"boot_id"`

	Client   string `json:"client,omitempty"` // 入站 token name（不是 key 值）
	Protocol string `json:"protocol"`         // openai | anthropic
	Stream   bool   `json:"stream"`

	Want     string `json:"want,omitempty"` // 客户端请求的 model 原文
	Virtual  bool   `json:"virtual"`
	WantKind string `json:"want_kind,omitempty"`

	Provider string `json:"provider,omitempty"`
	Model    string `json:"model,omitempty"`

	Status     int    `json:"status"`
	Outcome    string `json:"outcome"` // ok / client_canceled / exhausted / ...
	FailKind   string `json:"fail_kind,omitempty"`
	FailReason string `json:"fail_reason,omitempty"`

	TotalMs  int `json:"total_ms"`
	TTFTMs   int `json:"ttft_ms,omitempty"` // 流式首块；非流式=总耗时
	ReqBytes int `json:"req_bytes"`
	RespBytes int `json:"resp_bytes,omitempty"`

	InputTokens  int  `json:"input_tokens,omitempty"`
	OutputTokens int  `json:"output_tokens,omitempty"`
	UsageKnown   bool `json:"usage_known"`

	BudgetRaised   string `json:"budget_raised,omitempty"`
	ConfigHash     string `json:"config_hash,omitempty"`
	AdmissionMtime int64  `json:"admission_mtime,omitempty"`
	AdmissionSize  int64  `json:"admission_size,omitempty"`

	Attempts []router.Attempt `json:"attempts,omitempty"`
}

// Options 建 Recorder 的参数。
type Options struct {
	Dir           string
	RetentionDays int
	BootID        string
	ConfigHash    string
	AdmissionPath string
	Now           func() time.Time
}

// Recorder 按天轮转的 JSONL 写入器。nil / 禁用时所有方法安全。
type Recorder struct {
	dir           string
	retentionDays int
	bootID        string
	configHash    string
	admissionPath string
	now           func() time.Time

	mu       sync.Mutex
	f        *os.File
	day      string
	disabled bool
	warned   bool
	seq      uint64
}

// New 建 Recorder。Dir 为空 = 禁用（行为与接入前一致）。
func New(opts Options) *Recorder {
	r := &Recorder{
		dir:           opts.Dir,
		retentionDays: opts.RetentionDays,
		bootID:        opts.BootID,
		configHash:    opts.ConfigHash,
		admissionPath: opts.AdmissionPath,
		now:           opts.Now,
		disabled:      strings.TrimSpace(opts.Dir) == "",
	}
	if r.now == nil {
		r.now = time.Now
	}
	if r.retentionDays <= 0 {
		r.retentionDays = 30
	}
	if r.bootID == "" {
		r.bootID = NewBootID()
	}
	if r.disabled {
		log.Printf("telemetry: 未配置目录（MORETOKEN_TELEMETRY_DIR 为空）→ 运行记录禁用")
		return r
	}
	if err := os.MkdirAll(r.dir, 0o755); err != nil {
		log.Printf("telemetry: 建目录 %s 失败 → 运行记录禁用：%v", r.dir, err)
		r.disabled = true
		return r
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := r.ensureDayLocked(); err != nil {
		log.Printf("telemetry: 打开记录文件失败 → 运行记录禁用：%v", err)
		r.disabled = true
		return r
	}
	log.Printf("telemetry: 运行记录写入 %s（保留 %d 天）", r.dir, r.retentionDays)
	return r
}

// Enabled 是否真的在写。
func (r *Recorder) Enabled() bool { return r != nil && !r.disabled }

// ConfigHash 启动时算好的策略面指纹。
func (r *Recorder) ConfigHash() string {
	if r == nil {
		return ""
	}
	return r.configHash
}

// BootID 本次进程的标识（区分重启边界）。
func (r *Recorder) BootID() string {
	if r == nil {
		return ""
	}
	return r.bootID
}

// NextRequestID 生成本次进程内单调的 request id。
func (r *Recorder) NextRequestID() string {
	if r == nil {
		return ""
	}
	return fmt.Sprintf("%s-%06d", r.bootID, atomic.AddUint64(&r.seq, 1))
}

// AdmissionStamp 准入表版本（mtime/size）。文件缺失时返回 0,0。
func (r *Recorder) AdmissionStamp() (int64, int64) {
	if r == nil || r.admissionPath == "" {
		return 0, 0
	}
	st, err := os.Stat(r.admissionPath)
	if err != nil {
		return 0, 0
	}
	return st.ModTime().Unix(), st.Size()
}

// WriteRequest 写一条请求记录（补公共字段）。
func (r *Recorder) WriteRequest(req *Request) {
	if r == nil || req == nil || r.disabled {
		return
	}
	req.Type = "request"
	req.TS = r.now().Format(time.RFC3339Nano)
	req.BootID = r.bootID
	req.ConfigHash = r.configHash
	req.AdmissionMtime, req.AdmissionSize = r.AdmissionStamp()
	r.writeJSON(req)
}

// WriteMap 写一条非请求记录（boot/snapshot/shutdown 等）。
func (r *Recorder) WriteMap(kind string, m map[string]any) {
	if r == nil || r.disabled {
		return
	}
	if m == nil {
		m = map[string]any{}
	}
	m["type"] = kind
	m["ts"] = r.now().Format(time.RFC3339Nano)
	m["boot_id"] = r.bootID
	if r.configHash != "" {
		m["config_hash"] = r.configHash
	}
	if mt, sz := r.AdmissionStamp(); mt != 0 {
		m["admission_mtime"] = mt
		m["admission_size"] = sz
	}
	r.writeJSON(m)
}

func (r *Recorder) writeJSON(v any) {
	b, err := json.Marshal(v)
	if err != nil {
		r.warnOnce("序列化失败", err)
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := r.ensureDayLocked(); err != nil {
		r.warnLocked("打开记录文件失败", err)
		return
	}
	if _, err := r.f.Write(append(b, '\n')); err != nil {
		r.warnLocked("写入失败", err)
	}
}

// ensureDayLocked 跨日换文件并清理过期文件。调用方持锁。
func (r *Recorder) ensureDayLocked() error {
	day := r.now().Format("2006-01-02")
	if r.f != nil && r.day == day {
		return nil
	}
	if r.f != nil {
		_ = r.f.Close()
		r.f = nil
	}
	path := filepath.Join(r.dir, "telemetry-"+day+".jsonl")
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return err
	}
	r.f = f
	r.day = day
	r.pruneLocked()
	return nil
}

// pruneLocked 删除超过保留期的 telemetry-YYYY-MM-DD.jsonl。调用方持锁。
func (r *Recorder) pruneLocked() {
	entries, err := os.ReadDir(r.dir)
	if err != nil {
		return
	}
	cutoff := r.now().AddDate(0, 0, -r.retentionDays)
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasPrefix(name, "telemetry-") || !strings.HasSuffix(name, ".jsonl") {
			continue
		}
		day := strings.TrimSuffix(strings.TrimPrefix(name, "telemetry-"), ".jsonl")
		t, err := time.ParseInLocation("2006-01-02", day, r.now().Location())
		if err != nil {
			continue
		}
		if t.Before(cutoff) {
			_ = os.Remove(filepath.Join(r.dir, name))
		}
	}
}

func (r *Recorder) warnOnce(msg string, err error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.warnLocked(msg, err)
}

func (r *Recorder) warnLocked(msg string, err error) {
	if r.warned {
		return
	}
	r.warned = true
	log.Printf("telemetry: %s（后续同类错误不再刷屏）：%v", msg, err)
}

// Close 关闭当前文件。
func (r *Recorder) Close() {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.f != nil {
		_ = r.f.Close()
		r.f = nil
	}
}

// NewBootID 生成 8 位十六进制进程标识。
func NewBootID() string {
	var b [4]byte
	if _, err := rand.Read(b[:]); err == nil {
		return hex.EncodeToString(b[:])
	}
	return fmt.Sprintf("%08x", time.Now().UnixNano()&0xffffffff)
}

// ConfigHash 只哈希**策略面**：provider/base/format/tier/model/kinds/排除名单/阈值。
// 刻意不含 keys / auth_token —— 记录是给智能体读的，不能把密钥指纹写进去。
func ConfigHash(cfg *config.Config) string {
	if cfg == nil {
		return ""
	}
	var b strings.Builder
	fmt.Fprintf(&b, "listen=%s\n", cfg.Listen)
	fmt.Fprintf(&b, "admission=%s|ratio=%v\n", cfg.AdmissionPath, cfg.AdmissionMinRatio)
	fmt.Fprintf(&b, "ban=%s|paid=%s|minout=%s\n",
		ptrInt(cfg.RateLimitDailyBan), ptrBool(cfg.AllowPaidFallback), ptrInt(cfg.MinOutputTokens))
	fmt.Fprintf(&b, "exclude=%s\n", strings.Join(cfg.ExcludeModels, ","))
	for _, p := range cfg.Providers {
		fmt.Fprintf(&b, "p=%s|%s|%s|%s\n", p.ID, p.BaseURL, p.Format, p.Tier)
		for _, m := range p.Models {
			fmt.Fprintf(&b, "m=%s|%s|%d|%d\n",
				m.ID, strings.Join(m.Kinds, ","), m.ContextLength, m.MaxOutputTokens)
		}
	}
	sum := sha256.Sum256([]byte(b.String()))
	return hex.EncodeToString(sum[:8])
}

func ptrInt(p *int) string {
	if p == nil {
		return "-"
	}
	return fmt.Sprintf("%d", *p)
}

func ptrBool(p *bool) string {
	if p == nil {
		return "-"
	}
	return fmt.Sprintf("%t", *p)
}

type usageFields struct {
	InputTokens      int `json:"input_tokens"`
	OutputTokens     int `json:"output_tokens"`
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
}

// ParseUsage 从非流式响应或流式 chunk 里尽力提取 token 用量。
//
// 兼容 OpenAI（prompt_tokens/completion_tokens）与 Anthropic
// （input_tokens/output_tokens；message_start 在 message.usage 里）。
// 找不到返回 ok=false —— 绝不猜。
func ParseUsage(b []byte) (in, out int, ok bool) {
	if len(b) == 0 {
		return 0, 0, false
	}
	var probe struct {
		Usage   *usageFields `json:"usage"`
		Message *struct {
			Usage *usageFields `json:"usage"`
		} `json:"message"`
	}
	if err := json.Unmarshal(b, &probe); err != nil {
		return 0, 0, false
	}
	pick := func(u *usageFields) {
		if u == nil {
			return
		}
		if u.InputTokens != 0 {
			in, ok = u.InputTokens, true
		}
		if u.OutputTokens != 0 {
			out, ok = u.OutputTokens, true
		}
		if u.PromptTokens != 0 {
			in, ok = u.PromptTokens, true
		}
		if u.CompletionTokens != 0 {
			out, ok = u.CompletionTokens, true
		}
	}
	pick(probe.Usage)
	if probe.Message != nil {
		pick(probe.Message.Usage)
	}
	return in, out, ok
}
