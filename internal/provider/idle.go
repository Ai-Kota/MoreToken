package provider

import (
	"fmt"
	"io"
	"sync"
	"time"
)

// DefaultStreamIdle 流式响应的空闲上限：超过这段时间没有任何字节流入即判为挂死。
//
// 取值权衡：这是"上游挂了"与"上游在慢慢想"的分界。agnes 小请求 6–7s，
// 长推理可能更久，但**推理模型也不会连续 3 分钟一个字节都不吐**（SSE 有 keepalive/换行）。
// 取 3 分钟：足以放过任何合法的慢生成，又能把真挂死挡在会话冻死之前。
const DefaultStreamIdle = 180 * time.Second

// StreamIdle 流式空闲上限（包级变量，测试可改小）。
var StreamIdle = DefaultStreamIdle

// idleBody 给响应体加空闲看门狗。
//
// 为什么需要它：http.Client 不设总超时（那是为了让长生成不被切断），
// 于是"上游既不回字节、也不断开连接"时，Read 会**永远阻塞**。
// 对智能体来说挂死比报错糟得多——报错能重试/续写，挂死是会话原地冻住，
// 而"会话永不掉线"正是这个网关的核心承诺。
//
// 机制：每次读到字节就把定时器推后；定时器一响就连底层连接一起关掉，
// 让阻塞中的 Read 立刻返回错误（调用方归入 FailStreamMid，不重放，I7 语义不变）。
type idleBody struct {
	rc     io.ReadCloser
	idle   time.Duration
	timer  *time.Timer
	mu     sync.Mutex
	fired  bool
	closed bool
}

// newIdleBody 包一层看门狗。idle<=0 时用 StreamIdle。
func newIdleBody(rc io.ReadCloser, idle time.Duration) *idleBody {
	if idle <= 0 {
		idle = StreamIdle
	}
	b := &idleBody{rc: rc, idle: idle}
	b.timer = time.AfterFunc(idle, func() {
		b.mu.Lock()
		b.fired = true
		b.mu.Unlock()
		_ = rc.Close() // 关连接 → 阻塞中的 Read 立即出错
	})
	return b
}

func (b *idleBody) Read(p []byte) (int, error) {
	n, err := b.rc.Read(p)
	if n > 0 {
		b.timer.Reset(b.idle) // 有字节 → 再等一轮
	}
	if err != nil && b.wasFired() {
		return n, fmt.Errorf("流空闲超过 %s（上游未回字节也未断开）: %w", b.idle, err)
	}
	return n, err
}

func (b *idleBody) Close() error {
	b.timer.Stop()
	b.mu.Lock()
	b.closed = true
	b.mu.Unlock()
	return b.rc.Close()
}

func (b *idleBody) wasFired() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.fired
}

// wrapIdle 按需给响应体加看门狗（idle<=0 或未知类型时原样返回）。
func wrapIdle(rc io.ReadCloser, idle time.Duration) io.ReadCloser {
	if rc == nil {
		return rc
	}
	return newIdleBody(rc, idle)
}
