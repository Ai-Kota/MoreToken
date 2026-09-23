// assess-maturity: Evaluate project maturity level for progressive constraint selection
// Outputs maturity level (1-3) and recommended hooks.
// Usage: assess-maturity [--json]
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

type MaturityReport struct {
	Level       int              `json:"level"`
	LevelName   string           `json:"level_name"`
	Score       int              `json:"score"`
	MaxScore    int              `json:"max_score"`
	Details     []MaturityDetail `json:"details"`
	Hooks       []string         `json:"recommended_hooks"`
	DisabledHooks []string       `json:"disabled_hooks"`
}

type MaturityDetail struct {
	Category string `json:"category"`
	Found    bool   `json:"found"`
	Score    int    `json:"score"`
	MaxScore int    `json:"max_score"`
	Note     string `json:"note"`
}

func runAssessMaturity(args []string) {
	jsonOutput := len(args) > 0 && args[0] == "--json"

	report := assessProjectMaturity()

	if jsonOutput {
		data, _ := json.MarshalIndent(report, "", "  ")
		fmt.Println(string(data))
	} else {
		printMaturityReport(report)
	}
}

func assessProjectMaturity() MaturityReport {
	report := MaturityReport{
		MaxScore: 100,
	}

	// 1. 测试存在性 (0-25分)
	detail := checkTestPresence()
	report.Details = append(report.Details, detail)
	report.Score += detail.Score

	// 2. 测试覆盖率 (0-15分)
	detail = checkTestCoverageLevel()
	report.Details = append(report.Details, detail)
	report.Score += detail.Score

	// 3. 文档完整性 (0-20分)
	detail = checkDocsCompleteness()
	report.Details = append(report.Details, detail)
	report.Score += detail.Score

	// 4. 架构设计文档 (0-15分)
	detail = checkArchitectureDocs()
	report.Details = append(report.Details, detail)
	report.Score += detail.Score

	// 5. 代码结构规范 (0-15分)
	detail = checkCodeStructure()
	report.Details = append(report.Details, detail)
	report.Score += detail.Score

	// 6. 任务管理 (0-10分)
	detail = checkTaskManagement()
	report.Details = append(report.Details, detail)
	report.Score += detail.Score

	// Determine level
	switch {
	case report.Score >= 70:
		report.Level = 3
		report.LevelName = "严格"
		report.Hooks = []string{"env-protect", "check-read-before-write", "check-design-doc", "check-scope", "track-read", "clear-read"}
		report.DisabledHooks = []string{}
	case report.Score >= 40:
		report.Level = 2
		report.LevelName = "标准"
		report.Hooks = []string{"env-protect", "check-read-before-write", "check-scope", "track-read", "clear-read"}
		report.DisabledHooks = []string{"check-design-doc"}
	default:
		report.Level = 1
		report.LevelName = "宽松"
		report.Hooks = []string{"env-protect", "check-read-before-write", "track-read", "clear-read"}
		report.DisabledHooks = []string{"check-design-doc", "check-scope"}
	}

	return report
}

// checkTestPresence checks if test files exist.
// Scans multiple standard source roots (src/, internal/, cmd/, tests/, pkg/,
// lib/) plus repo-root test files, so standard Go/Python layouts are not
// under-scored by a src/-only scan.
func checkTestPresence() MaturityDetail {
	d := MaturityDetail{Category: "测试存在性", MaxScore: 25}

	roots := []string{"src", "internal", "cmd", "tests", "pkg", "lib"}

	// Go tests
	goTestCount := countTestFiles(roots, []string{"_test.go"})
	// Repo-root Go test files (one level, avoid re-counting roots).
	rootGo, _ := filepath.Glob("*_test.go")
	goTestCount += len(rootGo)

	// JS/TS tests
	jsTestCount := countTestFiles(roots, []string{".test.js", ".test.ts", ".spec.js", ".spec.ts"})

	// Python tests: files named test_*.py or *_test.py (suffix-based ext
	// matching can't catch the test_ prefix form).
	pyTestCount := countPythonTests(roots)
	rootPy, _ := filepath.Glob("test_*.py")
	pyTestCount += len(rootPy)

	totalTests := goTestCount + jsTestCount + pyTestCount

	if totalTests >= 10 {
		d.Found = true
		d.Score = 25
		d.Note = fmt.Sprintf("发现 %d 个测试文件", totalTests)
	} else if totalTests >= 3 {
		d.Found = true
		d.Score = 15
		d.Note = fmt.Sprintf("发现 %d 个测试文件（偏少）", totalTests)
	} else if totalTests > 0 {
		d.Found = true
		d.Score = 8
		d.Note = fmt.Sprintf("发现 %d 个测试文件（严重不足）", totalTests)
	} else {
		d.Score = 0
		d.Note = "未发现测试文件"
	}

	return d
}

// countTestFiles counts files matching any extension across the given roots.
func countTestFiles(roots []string, exts []string) int {
	n := 0
	for _, r := range roots {
		n += len(findFilesByExt(r, exts))
	}
	return n
}

// countPythonTests counts pytest files (test_*.py or *_test.py) across roots.
func countPythonTests(roots []string) int {
	n := 0
	for _, r := range roots {
		for _, f := range findFilesByExt(r, []string{".py"}) {
			base := filepath.Base(f)
			if strings.HasPrefix(base, "test_") || strings.HasSuffix(base, "_test.py") {
				n++
			}
		}
	}
	return n
}

// checkTestCoverageLevel checks test coverage
func checkTestCoverageLevel() MaturityDetail {
	d := MaturityDetail{Category: "测试覆盖率", MaxScore: 15}

	// Try to detect coverage from test output
	projectType := detectProjectType()
	if projectType == "unknown" {
		d.Score = 0
		d.Note = "无法检测项目类型"
		return d
	}

	// For Go, try to get coverage
	if projectType == "go" {
		d.Note = "Go 项目，覆盖率需通过 go test -cover 获取"
		d.Score = 5 // 基础分，实际覆盖率由 verify-task 检查
		return d
	}

	if projectType == "node" {
		d.Note = "Node 项目，覆盖率需通过 npm test --coverage 获取"
		d.Score = 5
		return d
	}

	if projectType == "python" {
		d.Note = "Python 项目，覆盖率需通过 pytest --cov 获取"
		d.Score = 5
		return d
	}

	d.Score = 0
	d.Note = "无法检测覆盖率"
	return d
}

// checkDocsCompleteness checks documentation completeness
func checkDocsCompleteness() MaturityDetail {
	d := MaturityDetail{Category: "文档完整性", MaxScore: 20}

	score := 0
	notes := []string{}

	if fileExists("README.md") {
		data, _ := os.ReadFile("README.md")
		content := string(data)
		// Check if README has real content (not template placeholder)
		if !strings.Contains(content, "[项目名]") && len(content) > 200 {
			score += 5
			notes = append(notes, "README.md 已填写")
		} else {
			notes = append(notes, "README.md 是模板占位符")
		}
	} else {
		notes = append(notes, "缺少 README.md")
	}

	if fileExists("docs/project/GOALS.md") {
		data, _ := os.ReadFile("docs/project/GOALS.md")
		content := string(data)
		if !strings.Contains(content, "[项目名]") && !strings.Contains(content, "[一句话描述]") {
			score += 5
			notes = append(notes, "GOALS.md 已填写")
		} else {
			notes = append(notes, "GOALS.md 是模板占位符")
		}
	}

	if fileExists("docs/project/ADR/ADR-001-scope.md") {
		data, _ := os.ReadFile("docs/project/ADR/ADR-001-scope.md")
		content := string(data)
		if strings.Contains(content, "已接受") {
			score += 5
			notes = append(notes, "ADR-001 已确认")
		}
	}

	if fileExists("docs/project/ADR/ADR-002-tech.md") {
		data, _ := os.ReadFile("docs/project/ADR/ADR-002-tech.md")
		content := string(data)
		if strings.Contains(content, "已接受") {
			score += 5
			notes = append(notes, "ADR-002 已确认")
		}
	}

	d.Score = score
	d.Note = strings.Join(notes, "; ")
	if score >= 15 {
		d.Found = true
	}

	return d
}

// checkArchitectureDocs checks architecture documentation
func checkArchitectureDocs() MaturityDetail {
	d := MaturityDetail{Category: "架构设计文档", MaxScore: 15}

	score := 0
	notes := []string{}

	if fileExists("docs/architecture/ARCHITECTURE.md") {
		data, _ := os.ReadFile("docs/architecture/ARCHITECTURE.md")
		content := string(data)
		if !strings.Contains(content, "[项目名]") && len(content) > 500 {
			score += 10
			notes = append(notes, "ARCHITECTURE.md 已填写")
		} else {
			notes = append(notes, "ARCHITECTURE.md 是模板占位符")
		}
	}

	modsDir := "docs/architecture/modules"
	if dirExists(modsDir) {
		mds := findFilesByExt(modsDir, []string{".md"})
		if len(mds) > 0 {
			score += 5
			notes = append(notes, fmt.Sprintf("发现 %d 个模块设计文档", len(mds)))
		}
	}

	d.Score = score
	d.Note = strings.Join(notes, "; ")
	if score >= 10 {
		d.Found = true
	}

	return d
}

// checkCodeStructure checks code organization
func checkCodeStructure() MaturityDetail {
	d := MaturityDetail{Category: "代码结构规范", MaxScore: 15}

	score := 0
	notes := []string{}

	// Check for standard directories
	if dirExists("src") || dirExists("internal") || dirExists("cmd") {
		score += 5
		notes = append(notes, "有标准源码目录")
	}

	// Check for config files
	if fileExists("go.mod") || fileExists("package.json") || fileExists("pyproject.toml") || fileExists("Cargo.toml") {
		score += 3
		notes = append(notes, "有项目配置文件")
	}

	// Check for lint config
	if fileExists(".golangci.yml") || fileExists(".eslintrc") || fileExists(".eslintrc.js") {
		score += 3
		notes = append(notes, "有 lint 配置")
	}

	// Check for CI
	if fileExists(".github/workflows/ci.yml") || fileExists(".github/workflows/ci.yaml") {
		score += 2
		notes = append(notes, "有 CI 配置")
	}

	// Check for .gitignore
	if fileExists(".gitignore") {
		score += 2
		notes = append(notes, "有 .gitignore")
	}

	d.Score = score
	d.Note = strings.Join(notes, "; ")
	if score >= 10 {
		d.Found = true
	}

	return d
}

// checkTaskManagement checks task management setup
func checkTaskManagement() MaturityDetail {
	d := MaturityDetail{Category: "任务管理", MaxScore: 10}

	score := 0
	notes := []string{}

	if fileExists("docs/tasks/TASKS.md") {
		data, _ := os.ReadFile("docs/tasks/TASKS.md")
		content := string(data)
		// Count completed tasks
		completed := strings.Count(content, "✅")
		inProgress := strings.Count(content, "🔄")
		total := completed + inProgress + strings.Count(content, "⬜")

		if total > 0 {
			score += 5
			notes = append(notes, fmt.Sprintf("TASKS.md: %d 个任务（%d完成/进行中）", total, completed))
		}

		if completed > 0 {
			score += 3
			notes = append(notes, "有已完成的任务记录")
		}
	}

	if fileExists("docs/tasks/SESSION-STATE.md") {
		data, _ := os.ReadFile("docs/tasks/SESSION-STATE.md")
		content := string(data)
		if !strings.Contains(content, "（待补充）") && len(content) > 200 {
			score += 2
			notes = append(notes, "SESSION-STATE.md 有实际内容")
		}
	}

	d.Score = score
	d.Note = strings.Join(notes, "; ")
	if score >= 7 {
		d.Found = true
	}

	return d
}

func printMaturityReport(report MaturityReport) {
	fmt.Println("==================================================")
	fmt.Println("📊 项目成熟度评估")
	fmt.Println("==================================================")
	fmt.Println()

	// Score bar
	filled := int(float64(report.Score) / float64(report.MaxScore) * 30)
	empty := 30 - filled
	scoreBar := fmt.Sprintf("[%s%s]", strings.Repeat("#", filled), strings.Repeat("-", empty))
	fmt.Printf("总分: %s %d/%d\n", scoreBar, report.Score, report.MaxScore)
	fmt.Printf("等级: Level %d — %s\n", report.Level, report.LevelName)
	fmt.Println()

	// Details
	fmt.Println("评分明细:")
	for _, d := range report.Details {
		status := "❌"
		if d.Found {
			status = "✅"
		}
		fmt.Printf("  %s %s: %d/%d  %s\n", status, d.Category, d.Score, d.MaxScore, d.Note)
	}
	fmt.Println()

	// Recommended hooks
	fmt.Println("推荐 Hook 配置:")
	fmt.Println("  启用:")
	for _, h := range report.Hooks {
		fmt.Printf("    ✅ %s\n", h)
	}
	if len(report.DisabledHooks) > 0 {
		fmt.Println("  禁用:")
		for _, h := range report.DisabledHooks {
			fmt.Printf("    ❌ %s\n", h)
		}
	}
	fmt.Println()

	// Recommendation
	fmt.Println("建议:")
	switch report.Level {
	case 1:
		fmt.Println("  项目成熟度较低，建议先补充测试和文档，再启用严格约束。")
		fmt.Println("  当前仅启用环境保护和先读后写检查。")
	case 2:
		fmt.Println("  项目有一定基础，可以启用标准约束。")
		fmt.Println("  建议为新模块补写设计文档。")
	case 3:
		fmt.Println("  项目成熟度高，可以启用全量约束。")
		fmt.Println("  所有 Hook 均已启用。")
	}

	fmt.Println()
	fmt.Println("==================================================")
	fmt.Println("使用: ai-guard generate-settings <level>  根据等级生成 settings.json")
}
