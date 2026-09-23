package provider

import (
	"net"
	"net/http"
	"time"
)

// 上游调用的默认超时。
//
// **没有 Total Timeout**——那会切断合法的长生成（Claude Code 一整轮可能跑几分钟）。
// 代价是挂死会变成无限等，所以改为在挂死真正发生的两处设限：
//
//	连接阶段（Dial/TLS）—— 连不上就别耗着
//	响应头阶段           —— 上游收下请求却不回响应头，这是最常见的挂死形态
//	响应体阶段           —— 由 idle.go 的空闲看门狗兜（流式与非流式都走它）
//
// ⚠️ 头阶段预算**不是常数**，按请求体大小缩放（HeaderBudgetFor），
// 且因为是 per-request 的，它不在 Transport 上——见 Invoke 里的实现与理由。
const (
	DefaultDialTimeout  = 15 * time.Second
	DefaultTLSHandshake = 20 * time.Second

	// DefaultHeaderBudget 小请求的头阶段基准预算。
	DefaultHeaderBudget = 90 * time.Second
	// headerScalePerMB 每 MB 请求体追加的头预算。
	headerScalePerMB = 60 * time.Second
)

// HeaderBudgetFor 按请求体大小算头阶段预算。
//
// **第一性原理：TTFT 是上下文的函数。** 实测（2026-09-22，agnes）：
//
//	60 KB  →   2.0s
//	978 KB →  36.5s
//	1.5 MB →  ~55s（上游繁忙时更高）
//
// 一个常数不可能同时服务这两种请求：90 秒对小请求是"挂死"，对大请求是"正常"。
// 2026-09-21/22 实测 14–25 条 503 **全部是 body 0.7–1.7MB 的请求撞上那个 90 秒**，
// 而它们完全合法。
//
// 这与本仓修过的"22 万 token 硬墙"（T-010）**同类**：按错误假设定的限制静默杀掉合法工作。
// 那次是长度上限，这次是时间上限；两者的错误假设都是"用小样本外推普遍规律"——
// 当初拿 60KB 测出 2 秒 → 推"首字节 6–7 秒" → 定 90 秒；
// 而我第一次排查时**又拿 60KB 去"证伪大 body 假设"**——用错了尺度，比不验证更危险。
//
// 取值：90s 基准（**不改变小请求的既有行为**）+ 每 MB 加 60s。
//
//	1 MB   → 150s（实测 37s 的约 4 倍余量）
//	1.7 MB → 192s
//
// 再放大就等于放弃挂死检测——挂死的请求要先把预算耗完才会 fallback 到下一候选。
func HeaderBudgetFor(bodyBytes int) time.Duration {
	if bodyBytes <= 0 {
		return DefaultHeaderBudget
	}
	mb := float64(bodyBytes) / float64(1<<20)
	return DefaultHeaderBudget + time.Duration(mb*float64(headerScalePerMB))
}

// NewTransport 建 Transport。
//
// **刻意不设 ResponseHeaderTimeout**：它是 Transport 级的常数，而头预算必须按请求算
// （HeaderBudgetFor 对 60KB 给 90s、对 1.5MB 给 192s——一个 Transport 装不下两个值）。
// 头预算改由 Invoke 用 per-request 定时器实现，好处是失败原因能带上**具体预算值**，
// 事后一眼看出当时用的是哪一档。
func NewTransport(dial, tls time.Duration) *http.Transport {
	return &http.Transport{
		Proxy: http.ProxyFromEnvironment,
		DialContext: (&net.Dialer{
			Timeout:   dial,
			KeepAlive: 30 * time.Second,
		}).DialContext,
		MaxIdleConns:          64,
		MaxIdleConnsPerHost:   16,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   tls,
		ExpectContinueTimeout: 5 * time.Second,
	}
}

// UpstreamClient 调用上游用的 HTTP client（默认超时）。
func UpstreamClient() *http.Client {
	return &http.Client{
		Transport: NewTransport(DefaultDialTimeout, DefaultTLSHandshake),
	}
}
