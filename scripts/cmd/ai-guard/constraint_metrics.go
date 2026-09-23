// constraint-metrics: Constraint effectiveness tracking
// Records and reports on constraint interception rates, false positives, and compliance.
// Usage: constraint-metrics [record|report|reset]
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"time"
)

const MetricsDir = ".claude/metrics"
const InterceptionsFile = MetricsDir + "/interceptions.json"
const ComplianceFile = MetricsDir + "/compliance.json"

type InterceptionRecord struct {
	Timestamp string `json:"timestamp"`
	Tool      string `json:"tool"`
	File      string `json:"file"`
	Rule      string `json:"rule"`
	Reason    string `json:"reason"`
	TaskID    string `json:"task_id"`
	Action    string `json:"action"`
}

type ComplianceRecord struct {
	Timestamp   string `json:"timestamp"`
	TaskID      string `json:"task_id"`
	Result      string `json:"result"`
	ChangedOnly bool   `json:"changed_only"`
	Duration    string `json:"duration"`
	TestsPassed int    `json:"tests_passed"`
	TestsFailed int    `json:"tests_failed"`
	LintErrors  int    `json:"lint_errors"`
	SecretsFound int   `json:"secrets_found"`
}

type Metrics struct {
	TotalInterceptions int
	Blocked            int
	Allowed            int
	Warned             int
	ByRule             map[string]int
	ByTool             map[string]int
	TotalVerifications int
	Passed             int
	Failed             int
	AvgDuration        string
}

func runConstraintMetrics(args []string) {
	if len(args) < 1 {
		fmt.Println("Usage: ai-guard constraint-metrics [record|report|reset]")
		fmt.Println("  record  - Record an interception event")
		fmt.Println("  report  - Show metrics summary")
		fmt.Println("  reset   - Clear all metrics")
		osExit(1)
	}

	os.MkdirAll(MetricsDir, 0755)

	switch args[0] {
	case "record":
		recordInterception(args)
	case "report":
		showMetricsReport()
	case "reset":
		resetMetrics()
	default:
		fmt.Printf("Unknown command: %s\n", args[0])
		osExit(1)
	}
}

func recordInterception(args []string) {
	if len(args) < 5 {
		fmt.Println("Usage: ai-guard constraint-metrics record <tool> <file> <rule> <action>")
		fmt.Println("  tool:   Hook tool name (check-read-before-write, check-design-doc, check-scope)")
		fmt.Println("  file:   File path that was intercepted")
		fmt.Println("  rule:   Rule that triggered (R1, R2, X5, etc.)")
		fmt.Println("  action: blocked, allowed, or warned")
		osExit(1)
	}

	record := InterceptionRecord{
		Timestamp: time.Now().Format("2006-01-02T15:04:05"),
		Tool:      args[1],
		File:      args[2],
		Rule:      args[3],
		Reason:    "",
		TaskID:    getMetricsTaskID(),
		Action:    args[4],
	}

	records := loadInterceptions()
	records = append(records, record)
	saveInterceptions(records)

	fmt.Printf("✅ Interception recorded: %s -> %s (%s)\n", record.Rule, record.File, record.Action)
}

func showMetricsReport() {
	fmt.Println("==================================================")
	fmt.Println("约束有效性报告")
	fmt.Println("==================================================")

	interceptions := loadInterceptions()
	compliance := loadCompliance()
	metrics := calculateMetrics(interceptions, compliance)

	fmt.Println("\n📊 拦截统计")
	fmt.Printf("  总拦截次数: %d\n", metrics.TotalInterceptions)
	fmt.Printf("  阻断 (blocked): %d\n", metrics.Blocked)
	fmt.Printf("  允许 (allowed): %d\n", metrics.Allowed)
	fmt.Printf("  警告 (warned): %d\n", metrics.Warned)

	if metrics.TotalInterceptions > 0 {
		blockRate := float64(metrics.Blocked) / float64(metrics.TotalInterceptions) * 100
		fmt.Printf("  阻断率: %.1f%%\n", blockRate)
	}

	fmt.Println("\n📋 按规则统计")
	rules := sortedMapKeys(metrics.ByRule)
	for _, rule := range rules {
		count := metrics.ByRule[rule]
		fmt.Printf("  %s: %d 次\n", rule, count)
	}

	fmt.Println("\n🔧 按工具统计")
	tools := sortedMapKeys(metrics.ByTool)
	for _, tool := range tools {
		count := metrics.ByTool[tool]
		fmt.Printf("  %s: %d 次\n", tool, count)
	}

	fmt.Println("\n✅ 验收统计")
	fmt.Printf("  总验收次数: %d\n", metrics.TotalVerifications)
	fmt.Printf("  通过: %d\n", metrics.Passed)
	fmt.Printf("  失败: %d\n", metrics.Failed)

	if metrics.TotalVerifications > 0 {
		passRate := float64(metrics.Passed) / float64(metrics.TotalVerifications) * 100
		fmt.Printf("  通过率: %.1f%%\n", passRate)
	}

	fmt.Println("\n📅 最近活动")
	recent := getRecentRecords(interceptions, 10)
	for _, r := range recent {
		fmt.Printf("  [%s] %s -> %s (%s)\n", r.Timestamp[:10], r.Rule, r.File, r.Action)
	}

	fmt.Println("\n==================================================")
}

func resetMetrics() {
	os.Remove(InterceptionsFile)
	os.Remove(ComplianceFile)
	fmt.Println("✅ 所有指标已清除")
}

func loadInterceptions() []InterceptionRecord {
	data, err := os.ReadFile(InterceptionsFile)
	if err != nil {
		return []InterceptionRecord{}
	}
	var records []InterceptionRecord
	json.Unmarshal(data, &records)
	return records
}

func saveInterceptions(records []InterceptionRecord) {
	if err := os.MkdirAll(MetricsDir, 0755); err != nil {
		fmt.Fprintf(os.Stderr, "⚠️  failed to create metrics dir: %v\n", err)
		return
	}
	data, _ := json.MarshalIndent(records, "", "  ")
	if err := os.WriteFile(InterceptionsFile, data, 0644); err != nil {
		fmt.Fprintf(os.Stderr, "⚠️  failed to save interceptions: %v\n", err)
	}
}

func loadCompliance() []ComplianceRecord {
	data, err := os.ReadFile(ComplianceFile)
	if err != nil {
		return []ComplianceRecord{}
	}
	var records []ComplianceRecord
	json.Unmarshal(data, &records)
	return records
}

func calculateMetrics(interceptions []InterceptionRecord, compliance []ComplianceRecord) Metrics {
	m := Metrics{
		ByRule: make(map[string]int),
		ByTool: make(map[string]int),
	}

	m.TotalInterceptions = len(interceptions)
	for _, r := range interceptions {
		switch r.Action {
		case "blocked":
			m.Blocked++
		case "allowed":
			m.Allowed++
		case "warned":
			m.Warned++
		}
		m.ByRule[r.Rule]++
		m.ByTool[r.Tool]++
	}

	m.TotalVerifications = len(compliance)
	for _, r := range compliance {
		if r.Result == "pass" {
			m.Passed++
		} else {
			m.Failed++
		}
	}

	return m
}

func getMetricsTaskID() string {
	trackerFile := ".claude/read_tracker.json"
	data, err := os.ReadFile(trackerFile)
	if err != nil {
		return ""
	}
	var tracker struct {
		TaskID string `json:"task_id"`
	}
	json.Unmarshal(data, &tracker)
	return tracker.TaskID
}

func getRecentRecords(records []InterceptionRecord, n int) []InterceptionRecord {
	if len(records) <= n {
		return records
	}
	return records[len(records)-n:]
}

func sortedMapKeys(m map[string]int) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
