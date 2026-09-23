package provider

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// TestIdleBody_StalledStreamAborts —— 流式开始后上游既不回字节也不断开时，
// 必须被主动中止，而不是让 Read 永远阻塞。
//
// 这是"会话永不掉线"的直接前提：网关不设总超时（为让长生成不被切断），
// 于是响应体阶段的静默只能靠这个看门狗兜。挂死对智能体比报错糟得多——
// 报错能重试/续写，挂死是会话原地冻住。
func TestIdleBody_StalledStreamAborts(t *testing.T) {
	pr, pw := io.Pipe()
	defer pw.Close()
	// 永不写入（模拟上游静默），也不会 EOF

	body := newIdleBody(pr, 120*time.Millisecond)

	start := time.Now()
	_, err := body.Read(make([]byte, 16))
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("上游静默时 Read 应返回错误，而不是永远阻塞")
	}
	if elapsed > 2*time.Second {
		t.Errorf("中止耗时 %v，看门狗没起作用", elapsed)
	}
	if !strings.Contains(err.Error(), "空闲") {
		t.Errorf("错误应说明是空闲超时，便于诊断，got %v", err)
	}
}

// TestIdleBody_ActiveStreamPasses 有持续数据流入时不受影响。
func TestIdleBody_ActiveStreamPasses(t *testing.T) {
	pr, pw := io.Pipe()
	body := newIdleBody(pr, 300*time.Millisecond)

	go func() {
		for i := 0; i < 5; i++ {
			time.Sleep(60 * time.Millisecond) // 每次都在 idle 之内
			_, _ = pw.Write([]byte("chunk"))
		}
		_ = pw.Close()
	}()

	var got int
	buf := make([]byte, 64)
	for {
		n, err := body.Read(buf)
		got += n
		if err != nil {
			if !errors.Is(err, io.EOF) {
				t.Fatalf("持续有数据的流不该报错，got %v", err)
			}
			break
		}
	}
	if got != 25 { // 5 * len("chunk")
		t.Errorf("读到 %d 字节，want 25", got)
	}
}

// TestIdleBody_ZeroUsesDefault idle<=0 时落到默认值（不变成"立即超时"）。
func TestIdleBody_ZeroUsesDefault(t *testing.T) {
	pr, _ := io.Pipe()
	b := newIdleBody(pr, 0)
	if b.idle != DefaultStreamIdle {
		t.Errorf("idle = %v, want %v", b.idle, DefaultStreamIdle)
	}
}

// TestHeaderBudget_HungUpstreamFails —— 上游收下请求却不回响应头
// （最常见的挂死形态）时，请求必须在**预算内**失败，不能无限等。
//
// 头预算从 Transport 挪到了 per-request（见 NewTransport 的说明：一个 Transport
// 装不下"60KB 给 90s、1.5MB 给 192s"两个值），所以这里改由 HeaderBudget 注入。
func TestHeaderBudget_HungUpstreamFails(t *testing.T) {
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-release // 收下请求，但既不写响应头也不返回
	}))
	t.Cleanup(func() { close(release); srv.Close() })

	start := time.Now()
	_, fail := Invoke(context.Background(), UpstreamClient(), Request{
		Format: "openai", BaseURL: srv.URL, APIKey: "k",
		Body: []byte("{}"), Kind: NonStreaming,
		HeaderBudget: 150 * time.Millisecond,
	})
	elapsed := time.Since(start)

	if fail.Kind == FailNone {
		t.Fatal("上游不回响应头时应失败，而不是无限等")
	}
	if elapsed > 3*time.Second {
		t.Errorf("失败耗时 %v，头预算没起作用", elapsed)
	}
	// 失败原因必须**说出是哪一种 canceled**：调用方断开与上游挂死含义相反，
	// 混成同一个 `context canceled` 会让排障跑偏（2026-09-21 就吃过这个亏）。
	if !strings.Contains(fail.Reason, "header budget") {
		t.Errorf("reason = %q，应明确指出是头预算烧完（而不是笼统的 context canceled）", fail.Reason)
	}
}

// TestHeaderBudgetFor_ScalesWithBody —— TTFT 是上下文的函数，预算必须跟着走。
//
// 血账（2026-09-21/22）：常数 90s 让 25 条 body 0.7–1.7MB 的**合法**请求被误杀
// （实测 978KB 正常响应要 36.5s，上游一忙就超 90s）。
func TestHeaderBudgetFor_ScalesWithBody(t *testing.T) {
	small := HeaderBudgetFor(60 << 10) // 60KB
	mid := HeaderBudgetFor(1 << 20)    // 1MB
	big := HeaderBudgetFor(1700 << 10) // 1.7MB

	// 小请求基本等于基准（公式对任意 body>0 都加零头，60KB 只加 ~3.5s）。
	// 断言"接近"而不是"相等"：公式是**刻意线性**的，逼它在小体积处返回精确基准
	// 等于给公式加一个没有理由的分段。
	if small < DefaultHeaderBudget || small > DefaultHeaderBudget+10*time.Second {
		t.Errorf("小请求预算 = %v，应贴近基准 %v（不实质改变既有行为）", small, DefaultHeaderBudget)
	}
	if mid != 150*time.Second {
		t.Errorf("1MB 预算 = %v，want 150s", mid)
	}
	if big <= mid {
		t.Errorf("预算必须随 body 单调增：1.7MB(%v) 应 > 1MB(%v)", big, mid)
	}
	if HeaderBudgetFor(0) != DefaultHeaderBudget {
		t.Error("body 未知时退回基准，不能给 0（那等于没有预算）")
	}
	// 实测 978KB ≈ 36.5s ⇒ 预算必须留出足够余量，否则又会误杀
	if mid < 100*time.Second {
		t.Errorf("1MB 的预算 %v 相对实测 36.5s 余量不足", mid)
	}
}

// TestUpstreamClient_NoTotalTimeout 上游 client **不能**设 Total Timeout——
// 那会切断合法的长生成（Claude Code 整轮可能跑几分钟）。
func TestUpstreamClient_NoTotalTimeout(t *testing.T) {
	c := UpstreamClient()
	if c.Timeout != 0 {
		t.Errorf("UpstreamClient().Timeout = %v，必须为 0（长生成不能被总超时切断）", c.Timeout)
	}
	tr, ok := c.Transport.(*http.Transport)
	if !ok {
		t.Fatal("Transport 类型不对")
	}
	// 头预算**刻意不设在 Transport 上**（它是常数，装不下按请求算的预算）。
	// 兜底改为：Invoke 里 HeaderBudget<=0 → DefaultHeaderBudget，恒大于 0。
	if tr.ResponseHeaderTimeout != 0 {
		t.Errorf("ResponseHeaderTimeout = %v，应为 0 —— 头预算改由 per-request 承担，"+
			"Transport 上再设一个常数会与它打架", tr.ResponseHeaderTimeout)
	}
	if DefaultHeaderBudget < 30*time.Second {
		t.Errorf("DefaultHeaderBudget = %v 太短，会误伤慢模型", DefaultHeaderBudget)
	}
}
