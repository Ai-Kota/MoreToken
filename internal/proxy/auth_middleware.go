package proxy

import (
	"context"
	"fmt"
	"net/http"
	"strings"
)

// authNameKey context key：中间件把命中的 token name 挂上，下游留痕用。
// 私有类型防跨包 key 碰撞（context 值的标准姿势）。
type authNameKeyType struct{}

var authNameKey = authNameKeyType{}

// AuthNameFromContext 取本次请求的 token name（未鉴权/豁免路径返回 ""）。
func AuthNameFromContext(ctx context.Context) string {
	name, _ := ctx.Value(authNameKey).(string)
	return name
}

// exemptPaths 鉴权豁免表。
//
// /health 永久豁免（T-024 §2 TH8）：dev-fleet 拿它判"要不要重启"（见 proxy.go
// 里 /doctor 独立通道的同款理由）。鉴权挡住探针 = 探针 401 = 判不健康 =
// 无谓重启 = 重启永动机。/health 只暴露池计数（无 key、无明文），公开无害。
var exemptPaths = map[string]bool{
	"/health": true,
}

// authMiddleware 入站鉴权（T-024）。规矩：
//
//   - s.auth == nil → 不挂本中间件（Handler() 里判断），零鉴权现状不变
//   - 每请求先 ReloadIfChanged：吊销/轮换经 mtime 热加载，**下一请求即生效**
//   - 双协议头：Authorization: Bearer <t>（OpenAI 系）或 x-api-key: <t>（Anthropic 系）；
//     两头都带且不一致 → 取 Authorization（更标准的那个说了算，不猜）
//   - 拒绝必留痕（T-016 教训：吸收即销毁证据）：401 进决策日志，带原因与 name
//   - 401 body 按端点协议成形：SDK 只认自家错误形状（T-009 修 401 透传的同款教训）
// AuthWrap 把鉴权中间件包在任意 handler 外（导出：main.go 的 /decisions
// 注册在外层 mux 上，不经过 Handler()，须单独包一层才能同样受保护）。
// 未配置鉴权 → 原样返回 next（零行为变化）。
func (s *Server) AuthWrap(next http.Handler) http.Handler {
	if !s.authEnabled() {
		return next
	}
	return s.authMiddleware(next)
}

func (s *Server) authMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if exemptPaths[req.URL.Path] {
			next.ServeHTTP(w, req)
			return
		}
		s.auth.ReloadIfChanged()
		tok, header := extractToken(req)
		name, ok := s.auth.Verify(tok)
		if !ok {
			reason := "auth: missing or invalid token"
			if name != "" {
				// Verify 命中了条目但状态不对：说清是吊销还是过期，排查不用猜
				reason = fmt.Sprintf("auth: token %q revoked or expired", name)
			} else if tok == "" {
				reason = "auth: no token presented (want Authorization: Bearer mt_… or x-api-key)"
			}
			s.recordAuthReject(req.URL.Path, header, reason)
			writeAuthError(w, req.URL.Path, reason)
			return
		}
		if name != "" {
			s.auth.TouchAsync(name) // last_used 回写：异步、失败不拒（审计不反噬可用性）
		}
		next.ServeHTTP(w, req.WithContext(context.WithValue(req.Context(), authNameKey, name)))
	})
}

// extractToken 从两种协议头里取 token。返回 (token, 来源头名)——来源头进留痕，
// 排查"客户端到底发了什么"时不用再抓包。
func extractToken(req *http.Request) (string, string) {
	if h := req.Header.Get("Authorization"); h != "" {
		// 只认 Bearer 方案；畸形头（无 Bearer 前缀）按"没送"处理（矩阵"错误"格）。
		if tok, found := strings.CutPrefix(h, "Bearer "); found {
			return strings.TrimSpace(tok), "Authorization"
		}
		return "", "Authorization"
	}
	if h := req.Header.Get("x-api-key"); h != "" {
		return strings.TrimSpace(h), "x-api-key"
	}
	return "", ""
}

// recordAuthReject 401 留痕。ProviderID 固定 "auth"：决策日志里一眼分出
// "路由层失败"与"门口就拒了"两类。
func (s *Server) recordAuthReject(path, header, reason string) {
	if s.declog == nil {
		return
	}
	if header != "" {
		reason += " (header: " + header + ")"
	}
	s.declog.Record(DecisionEntry{
		ProviderID: "auth",
		Status:     http.StatusUnauthorized,
		Reason:     reason + " path=" + path,
	})
}

// writeAuthError 401 body 按端点协议成形。
//
//	/v1/messages         → anthropic 形状（Claude Code 的错误映射认这个）
//	其余（openai 系端点） → openai 形状
func writeAuthError(w http.ResponseWriter, path, reason string) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("WWW-Authenticate", "Bearer")
	w.WriteHeader(http.StatusUnauthorized)
	if path == "/v1/messages" {
		fmt.Fprintf(w, `{"type":"error","error":{"type":"authentication_error","message":%q}}`, reason)
		return
	}
	fmt.Fprintf(w, `{"error":{"message":%q,"type":"invalid_request_error","code":"invalid_api_key"}}`, reason)
}

// authEnabled 供 Handler() 判空。抽出来是为了让"未配置鉴权"路径**不构造中间件**——
// 兼容模式零开销、零行为变化。
func (s *Server) authEnabled() bool { return s.auth != nil }
