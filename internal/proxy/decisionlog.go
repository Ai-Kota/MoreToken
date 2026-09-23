package proxy

import (
	"sync"
	"time"

	"moretoken/internal/router"
)

// DecisionEntry 一次路由决策的留痕（T-003 切换留痕；密钥安全——不含 key 值）。
type DecisionEntry struct {
	At         time.Time `json:"at"`
	ProviderID string    `json:"provider_id"`
	Retried    bool      `json:"retried"`
	Reason     string    `json:"reason,omitempty"`
	Status     int       `json:"status"`
	// Model 虚拟模型（auto / auto:<kind>）本轮实际落到的具体模型 id。
	// 这是"切换可见"的载体：客户端全程只认 auto:reasoning，但能从这里发现
	// 上游模型换了（进而决定是否重述关键约束）。字面模型请求时为空。
	Model string `json:"model,omitempty"`
	// Want 客户端**请求的 model 原文**（虚拟模型如 "auto:reasoning"；字面模型则是模型名）。
	//
	// 填写口径按路径不同（2026-09-20 明确）：成功/4xx 透传路径只在**虚拟模型**时填
	// （字面模型连 Model 也为空，与既有的"字面模型零行为变化"一致）；
	// 503 路径**恒填**——那正是"没有响应"的事故里最需要先知道的一件事。
	Want string `json:"want,omitempty"`

	// ElapsedMs / BodyBytes **只在"值得看"的成功请求上填**（见 Server.noteIfNoteworthy）。
	//
	// 为什么不在每条上都填：`/decisions` 是 1024 条的内存环，全填会把真正有用的掩埋掉
	//（流式生成平均 22s，60s 阈值以下全是噪音）。它们存在的意义是**证明修复生效**：
	// 出现一条 `elapsed≈120s + body≈1.5MB + status:200`，就是"旧代码必然 503、现在成功"的直接证据。
	ElapsedMs int `json:"elapsed_ms,omitempty"`
	BodyBytes int `json:"body_bytes,omitempty"`

	// Attempts 逐个候选的失败留痕（只在"没有响应"的失败路径填）。
	//
	// 为什么必须有：聚合原因（`exhausted ... (tried 2, limit 4)`）回答不了
	// "**是谁坏的、怎么坏的**"。2026-09-21 实测有 7 次突发降级、其中一次三个候选
	// 同时失败——而当时的留痕一个字都没记，那个问题至今【未核实】。
	Attempts []router.Attempt `json:"attempts,omitempty"`
}

// DecisionLog 有界 ring buffer（决策日志 / 切换留痕）。
// T-003 落内存（纯标准库零依赖）；T-005 WorkBoard 接入时可经 /decisions 端点暴露。
type DecisionLog struct {
	mu     sync.Mutex
	buf    []DecisionEntry
	next   int
	full   bool
	capN   int
}

// NewDecisionLog 建容量 capN 的日志（capN<=0 → 默认 1024）。
func NewDecisionLog(capN int) *DecisionLog {
	if capN <= 0 {
		capN = 1024
	}
	return &DecisionLog{buf: make([]DecisionEntry, capN), capN: capN}
}

// Record 记一条（线程安全）。
func (l *DecisionLog) Record(e DecisionEntry) {
	if l == nil {
		return
	}
	if e.At.IsZero() {
		e.At = time.Now()
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	l.buf[l.next] = e
	l.next = (l.next + 1) % l.capN
	if l.next == 0 {
		l.full = true
	}
}

// Recent 返回最近 n 条（按时间正序；n<=0 = 全部）。
func (l *DecisionLog) Recent(n int) []DecisionEntry {
	if l == nil {
		return nil
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	total := l.capN
	if l.full == false {
		total = l.next
	}
	if n <= 0 || n > total {
		n = total
	}
	out := make([]DecisionEntry, 0, n)
	// 从 oldest 起取最近 n 条：oldest 下标 = (l.next - n + cap) % cap（full 时 next 是写指针）。
	start := (l.next - n + l.capN) % l.capN
	for i := 0; i < n; i++ {
		out = append(out, l.buf[(start+i)%l.capN])
	}
	return out
}

// Len 当前条数（未满 = next，满 = capN）。
func (l *DecisionLog) Len() int {
	if l == nil {
		return 0
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.full {
		return l.capN
	}
	return l.next
}
