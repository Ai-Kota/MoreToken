// project-assess: Pre-project risk and cost assessment
// Evaluates project difficulty, cost, risks, and environment impact.
// Usage: project-assess "项目描述" [--json]
package main

import (
	"encoding/json"
	"fmt"
	"strings"
)

type Assessment struct {
	ProjectName     string   `json:"project_name"`
	Difficulty      int      `json:"difficulty"`
	EstimatedHours  int      `json:"estimated_hours"`
	Risks           []string `json:"risks"`
	EnvImpact       string   `json:"env_impact"`
	NeedsApproval   []string `json:"needs_approval"`
	Recommendation  string   `json:"recommendation"`
	RecommendReason string   `json:"recommend_reason"`
}

func runProjectAssess(args []string) {
	if len(args) < 1 {
		fmt.Println("Usage: ai-guard project-assess \"项目描述\" [--json]")
		fmt.Println()
		fmt.Println("Examples:")
		fmt.Println("  ai-guard project-assess \"构建一个 REST API 服务\"")
		fmt.Println("  ai-guard project-assess \"添加用户认证模块\" --json")
		osExit(1)
	}

	description := args[0]
	jsonOutput := len(args) > 1 && args[1] == "--json"

	assessment := assessProject(description)

	if jsonOutput {
		data, _ := json.MarshalIndent(assessment, "", "  ")
		fmt.Println(string(data))
	} else {
		printAssessmentReport(assessment)
	}
}

func assessProject(description string) Assessment {
	a := Assessment{
		ProjectName:   truncateStr(description, 50),
		Risks:         []string{},
		NeedsApproval: []string{},
	}

	desc := strings.ToLower(description)

	a.Difficulty = estimateDifficulty(desc)
	a.EstimatedHours = estimateHours(desc, a.Difficulty)
	a.Risks = identifyRisks(desc)
	a.EnvImpact = assessEnvImpact(desc)
	a.NeedsApproval = identifyApprovalNeeds(desc)
	a.Recommendation, a.RecommendReason = makeRecommendation(a)

	return a
}

func estimateDifficulty(desc string) int {
	score := 1

	simpleWords := []string{"hello", "world", "test", "demo", "simple", "basic", "todo"}
	for _, w := range simpleWords {
		if strings.Contains(desc, w) {
			score = intMax(score, 1)
		}
	}

	mediumWords := []string{"api", "crud", "database", "auth", "login", "user", "admin"}
	for _, w := range mediumWords {
		if strings.Contains(desc, w) {
			score = intMax(score, 2)
		}
	}

	complexWords := []string{"microservice", "distributed", "real-time", "websocket", "grpc", "kafka", "redis", "cluster"}
	for _, w := range complexWords {
		if strings.Contains(desc, w) {
			score = intMax(score, 3)
		}
	}

	veryComplexWords := []string{"machine learning", "deep learning", "neural", "compiler", "kernel", "driver", "blockchain"}
	for _, w := range veryComplexWords {
		if strings.Contains(desc, w) {
			score = intMax(score, 4)
		}
	}

	extremeWords := []string{"operating system", "database engine", "programming language", "distributed system", "consensus algorithm"}
	for _, w := range extremeWords {
		if strings.Contains(desc, w) {
			score = intMax(score, 5)
		}
	}

	return score
}

func estimateHours(desc string, difficulty int) int {
	baseHours := map[int]int{
		1: 2,
		2: 8,
		3: 24,
		4: 80,
		5: 200,
	}
	return baseHours[difficulty]
}

func identifyRisks(desc string) []string {
	risks := []string{}

	if strings.Contains(desc, "npm") || strings.Contains(desc, "node") || strings.Contains(desc, "javascript") {
		risks = append(risks, "Node.js 依赖链较长，可能遇到版本冲突")
	}
	if strings.Contains(desc, "python") || strings.Contains(desc, "pip") {
		risks = append(risks, "Python 依赖管理可能遇到虚拟环境问题")
	}

	integrationWords := []string{"third-party", "external api", "webhook", "oauth", "payment", "sms", "email"}
	for _, w := range integrationWords {
		if strings.Contains(desc, w) {
			risks = append(risks, "依赖第三方服务，可能遇到 API 限制或变更")
			break
		}
	}

	securityWords := []string{"auth", "password", "token", "secret", "encrypt", "ssl", "tls", "payment"}
	for _, w := range securityWords {
		if strings.Contains(desc, w) {
			risks = append(risks, "涉及安全敏感操作，需要额外审查")
			break
		}
	}

	perfWords := []string{"high traffic", "concurrent", "real-time", "streaming", "video", "audio"}
	for _, w := range perfWords {
		if strings.Contains(desc, w) {
			risks = append(risks, "性能要求较高，可能需要优化")
			break
		}
	}

	dataWords := []string{"migration", "database", "schema", "data", "backup", "restore"}
	for _, w := range dataWords {
		if strings.Contains(desc, w) {
			risks = append(risks, "涉及数据操作，需要备份和回滚策略")
			break
		}
	}

	if len(risks) == 0 {
		risks = append(risks, "未识别到显著风险")
	}

	return risks
}

func assessEnvImpact(desc string) string {
	highImpact := []string{"install", "global", "system", "service", "daemon", "cron"}
	for _, w := range highImpact {
		if strings.Contains(desc, w) {
			return "high"
		}
	}

	mediumImpact := []string{"docker", "container", "database", "redis", "nginx"}
	for _, w := range mediumImpact {
		if strings.Contains(desc, w) {
			return "medium"
		}
	}

	return "low"
}

func identifyApprovalNeeds(desc string) []string {
	needs := []string{}

	approvalKeywords := map[string]string{
		"install":    "需要安装新依赖",
		"database":   "需要修改数据库结构",
		"deploy":     "需要部署到服务器",
		"ci/cd":      "需要修改 CI/CD 配置",
		"security":   "涉及安全配置",
		"payment":    "涉及支付集成",
		"auth":       "涉及认证系统",
		"docker":     "需要 Docker 操作",
		"kubernetes": "需要 K8s 操作",
		"terraform":  "需要基础设施变更",
	}

	for keyword, reason := range approvalKeywords {
		if strings.Contains(desc, keyword) {
			needs = append(needs, reason)
		}
	}

	return needs
}

func makeRecommendation(a Assessment) (string, string) {
	if a.Difficulty >= 4 && a.EnvImpact == "high" {
		return "revise", "项目难度高且环境影响大，建议拆分为更小的迭代"
	}
	if a.Difficulty >= 4 && len(a.Risks) > 3 {
		return "revise", "项目风险较多，建议先做原型验证"
	}
	if len(a.NeedsApproval) > 3 {
		return "revise", "需要大量人工审批，建议简化需求或分阶段实施"
	}
	if a.Difficulty >= 3 {
		return "proceed", "项目可行，建议按阶段实施"
	}
	return "proceed", "项目简单，可以直接开始"
}

func truncateStr(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen-3] + "..."
}

func intMax(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func printAssessmentReport(a Assessment) {
	fmt.Println("==================================================")
	fmt.Println("📋 项目评估报告")
	fmt.Println("==================================================")
	fmt.Println()
	fmt.Printf("项目: %s\n", a.ProjectName)
	fmt.Printf("难度: %s (%d/5)\n", difficultyBar(a.Difficulty), a.Difficulty)
	fmt.Printf("预估工时: %d 小时\n", a.EstimatedHours)
	fmt.Printf("环境影响: %s\n", envImpactLabel(a.EnvImpact))
	fmt.Println()

	fmt.Println("⚠️  风险识别:")
	for _, r := range a.Risks {
		fmt.Printf("  • %s\n", r)
	}
	fmt.Println()

	if len(a.NeedsApproval) > 0 {
		fmt.Println("🔐 需要人工审批:")
		for _, n := range a.NeedsApproval {
			fmt.Printf("  • %s\n", n)
		}
		fmt.Println()
	}

	fmt.Printf("📊 建议: %s\n", a.Recommendation)
	fmt.Printf("   %s\n", a.RecommendReason)
	fmt.Println()
	fmt.Println("==================================================")
}

func difficultyBar(d int) string {
	filled := strings.Repeat("█", d)
	empty := strings.Repeat("░", 5-d)
	return filled + empty
}

func envImpactLabel(e string) string {
	switch e {
	case "low":
		return "🟢 低（仅项目目录内）"
	case "medium":
		return "🟡 中（需要容器/服务）"
	case "high":
		return "🔴 高（需要系统级安装）"
	default:
		return "❓ 未知"
	}
}
