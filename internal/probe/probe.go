// Package probe 免费层的健康探测（状态机 I4/I5 的输入）。
//
// 探测语义：对 provider 发一个最小请求，2xx 即健康。
// 关键在于**取哪把 key**——见 Provider 的说明：只认第一把会让探测退化成单点。
package probe

import (
	"context"
	"net/http"
	"strings"

	"moretoken/internal/config"
	"moretoken/internal/provider"
)

// DefaultAttempts 单次探测最多试几把 key。
//
// 上限的意义：provider 全挂时每轮最多 DefaultAttempts 次上游调用，
// 而不是把池里几十把全打一遍（那是自造的流量风暴）。
const DefaultAttempts = 3

// Keys 取 provider 的全部真实 key（跳过未解析的 env:/vault: 占位）。
func Keys(p config.Provider) []string {
	out := make([]string, 0, len(p.Keys))
	for _, k := range p.Keys {
		if config.IsPlaceholder(k) {
			continue
		}
		out = append(out, k)
	}
	return out
}

// probeModel 选探测用的模型：该 provider 的第一个模型（models 顺序即偏好顺序，
// 与 router 侧 kind="" 的解析口径一致）。
//
// 为什么必须是真模型名：上游拒绝未知模型（agnes 503 model_not_found / xkiro 404
// model does not exist），拿假名探测等于恒判不健康，状态机就永远回不到免费层。
// 无模型 = 配置错误，按不健康处理（与"无真实凭据"同一条口径）。
func probeModel(p config.Provider) (string, bool) {
	if len(p.Models) == 0 {
		return "", false
	}
	if m := strings.TrimSpace(p.Models[0].ID); m != "" {
		return m, true
	}
	return "", false
}

// Provider 判定一个 provider 当下是否健康。
//
// 从 round 指定的起点轮换取最多 attempts 把 key，**命中任意一把即算健康**。
//
// 为什么不能只试第一把（旧行为）：那把一旦失效，探测就永远失败 →
// 健康窗口永远累积不到阈值 → 一旦进了付费层就再也回不到免费，
// 哪怕其余几十把全是好的。那是运气不是设计。
//
// round 每轮推进，让所有 key 都有机会被采到（等价于把"第一把是不是好的"
// 稀释成"任意一把是不是好的"）。
func Provider(ctx context.Context, client *http.Client, p config.Provider, round uint64, attempts int) bool {
	if attempts <= 0 {
		attempts = DefaultAttempts
	}
	keys := Keys(p)
	if len(keys) == 0 {
		return false // 无真实凭据 → 不健康（不进付费回归判定）
	}
	model, ok := probeModel(p)
	if !ok {
		return false // 无模型可探 → 同样不健康（拿假名探等于恒判不健康）
	}
	start := int(round % uint64(len(keys)))
	for i := 0; i < attempts && i < len(keys); i++ {
		res, fail := provider.Invoke(ctx, client, provider.Request{
			Format:  string(p.Format),
			BaseURL: p.BaseURL,
			APIKey:  keys[(start+i)%len(keys)],
			Body:    provider.ProbeBody(model),
			Kind:    provider.NonStreaming,
		})
		if provider.IsHealthy(res, fail) {
			return true
		}
	}
	return false
}
