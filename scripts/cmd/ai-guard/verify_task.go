// verify-task: Task verification gate
// Runs all quality gates and updates task status.
// Usage: verify-task T-XXX [--changed-only]
package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

const (
	VTTasksFile   = "docs/tasks/TASKS.md"
	VTSessionFile = "docs/tasks/SESSION-STATE.md"
	VTReqsFile    = "docs/tasks/REQUIREMENTS.md"
)

var vtChangedOnly = false

func runVerifyTask(args []string) {
	if len(args) < 1 {
		fmt.Println("Usage: ai-guard verify-task T-XXX [--changed-only]")
		osExit(1)
	}

	taskID := args[0]
	if len(args) > 1 && args[1] == "--changed-only" {
		vtChangedOnly = true
	}

	fmt.Println("==================================================")
	fmt.Printf("任务验收检查: %s\n", taskID)
	if vtChangedOnly {
		fmt.Println("  模式: 增量测试（仅测试变更文件）")
	}
	fmt.Println("==================================================")

	// 1. Check task status
	fmt.Println("1️⃣ 检查任务状态...")
	if !fileExists(VTTasksFile) {
		fmt.Println("❌ TASKS.md 不存在")
		osExit(1)
	}
	content, _ := readFile(VTTasksFile)
	if !strings.Contains(content, taskID) {
		fmt.Printf("❌ 任务 %s 不存在于 TASKS.md\n", taskID)
		osExit(1)
	}
	taskLine := findTaskLine(content, taskID)
	if strings.Contains(taskLine, "🔄") {
		fmt.Println("   ✅ 任务状态: 🔄 进行中")
	} else if strings.Contains(taskLine, "⬜") {
		fmt.Println("   ⚠️  任务状态: ⬜ 待办")
	}

	projectType := detectProjectType()
	fmt.Printf("   项目类型: %s\n", projectType)

	// 2. Run tests
	fmt.Println("\n2️⃣ 运行测试...")
	if !runVerifyTests(projectType) {
		osExit(1)
	}

	// 3. Lint check
	fmt.Println("\n3️⃣ Lint 检查...")
	runVerifyLint(projectType)

	// 4. Check hardcoded secrets
	fmt.Println("\n4️⃣ 检查硬编码敏感信息...")
	checkVerifySecrets()

	// 5. Check doc sync
	fmt.Println("\n5️⃣ 检查文档同步...")
	checkDocSync()

	// 5b. Evidence chain: acceptance criteria (DoD) must all be checked off.
	fmt.Println("\n   📋 核销验收标准（DoD）...")
	if !checkTaskEvidence(taskID) {
		fmt.Println("❌ 验收标准未全部核销，任务不能标记完成")
		osExit(2)
	}
	checkMatrixCoverage()

	// 6. Update TASKS.md
	fmt.Println("\n6️⃣ 更新任务状态...")
	updateTasksStatus(taskID)

	// 7. Update SESSION-STATE.md
	fmt.Println("\n7️⃣ 更新会话状态...")
	updateSessionState(taskID)

	// 8. Update REQUIREMENTS.md
	fmt.Println("\n8️⃣ 检查需求追踪...")
	updateRequirements(taskID)

	// 9. Record metrics
	fmt.Println("\n9️⃣ 记录验收指标...")
	recordMetrics(taskID, true)

	fmt.Println("\n==================================================")
	fmt.Printf("✅ 任务 %s 验收通过！\n", taskID)
	fmt.Println("==================================================")
	fmt.Println("\n后续步骤:")
	fmt.Printf("  1. git add -A && git commit -m 'feat: complete %s'\n", taskID)
	fmt.Println("  2. git push origin feature/...")
	fmt.Println("  3. 创建 PR 进行 Code Review")
}

func runVerifyTests(projectType string) bool {
	var cmd *exec.Cmd

	if vtChangedOnly {
		changedFiles := getChangedFiles()
		if len(changedFiles) == 0 {
			fmt.Println("   ⚠️  无变更文件，跳过测试")
			return true
		}
		fmt.Printf("   变更文件: %d 个\n", len(changedFiles))

		switch projectType {
		case "go":
			packages := getChangedPackages(changedFiles, ".go")
			if len(packages) == 0 {
				fmt.Println("   ⚠️  无 Go 文件变更，跳过测试")
				return true
			}
			args := append([]string{"test", "-p", "1"}, packages...)
			cmd = exec.Command("go", args...)
		case "node":
			cmd = exec.Command("npm", "test")
		case "python":
			pyFiles := filterByExt(changedFiles, ".py")
			if len(pyFiles) == 0 {
				fmt.Println("   ⚠️  无 Python 文件变更，跳过测试")
				return true
			}
			cmd = exec.Command("pytest", pyFiles...)
		default:
			fmt.Println("   ⚠️  未识别项目类型，跳过测试")
			return true
		}
	} else {
		switch projectType {
		case "go":
			// -p 1 serializes package tests to avoid cross-package cwd races
			// when suites swap os.Chdir (see scripts/test-all.sh).
			cmd = exec.Command("go", "test", "-p", "1", "./...")
		case "node":
			cmd = exec.Command("npm", "test")
		case "python":
			cmd = exec.Command("pytest")
		default:
			fmt.Println("   ⚠️  未识别项目类型，跳过测试")
			return true
		}
	}

	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		fmt.Println("❌ 测试失败")
		return false
	}
	fmt.Println("   ✅ 测试通过")
	return true
}

func runVerifyLint(projectType string) {
	var cmd *exec.Cmd
	switch projectType {
	case "go":
		if _, err := exec.LookPath("golangci-lint"); err != nil {
			fmt.Println("   ⚠️  未安装 golangci-lint，跳过")
			return
		}
		cmd = exec.Command("golangci-lint", "run")
	case "node":
		if !fileExists("package.json") {
			fmt.Println("   ⚠️  未配置 Lint，跳过")
			return
		}
		content, _ := readFile("package.json")
		if !strings.Contains(content, `"lint"`) {
			fmt.Println("   ⚠️  未配置 Lint，跳过")
			return
		}
		cmd = exec.Command("npm", "run", "lint")
	default:
		fmt.Println("   ⚠️  未配置 Lint 工具，跳过")
		return
	}

	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		fmt.Println("❌ Lint 失败")
	} else {
		fmt.Println("   ✅ Lint 通过")
	}
}

func checkVerifySecrets() int {
	patterns := []*regexp.Regexp{
		regexp.MustCompile(`(?i)password\s*[:=]\s*["'][^"']+["']`),
		regexp.MustCompile(`(?i)secret\s*[:=]\s*["'][^"']+["']`),
		regexp.MustCompile(`(?i)api[_-]?key\s*[:=]\s*["'][^"']+["']`),
		regexp.MustCompile(`(?i)token\s*[:=]\s*["'][^"']+["']`),
		regexp.MustCompile(`(?i)private[_-]?key\s*[:=]\s*["'][^"']+["']`),
	}

	exts := []string{".go", ".ts", ".js", ".py", ".yaml", ".yml"}
	files := findFilesByExt("src", exts)
	findings := []string{}

	for _, f := range files {
		data, err := readFile(f)
		if err != nil {
			continue
		}
		lines := strings.Split(data, "\n")
		for i, line := range lines {
			for _, re := range patterns {
				if re.MatchString(line) {
					findings = append(findings, fmt.Sprintf("  %s:%d", f, i+1))
					break
				}
			}
		}
	}

	if len(findings) > 0 {
		fmt.Println("❌ 发现疑似硬编码敏感信息:")
		for _, f := range findings {
			if len(findings) > 10 {
				fmt.Println(f)
				fmt.Println("   ... (超过 10 个，已截断)")
				break
			}
			fmt.Println(f)
		}
	} else {
		fmt.Println("   ✅ 无硬编码敏感信息")
	}
	return len(findings)
}

func checkDocSync() {
	cmd := exec.Command("git", "diff", "--name-only", "HEAD")
	out, err := cmd.Output()
	if err != nil || len(out) == 0 {
		fmt.Println("   ⚠️  无 Git 变更记录")
		return
	}
	fmt.Println("  变更文件:")
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	for i, f := range lines {
		if i >= 10 {
			fmt.Printf("    ... (共 %d 个)\n", len(lines))
			break
		}
		fmt.Printf("    %s\n", f)
	}
}

func updateTasksStatus(taskID string) {
	content, _ := readFile(VTTasksFile)
	lines := strings.Split(content, "\n")
	for i, line := range lines {
		if strings.Contains(line, taskID) && strings.Contains(line, "🔄") {
			lines[i] = strings.Replace(line, "🔄", "✅", 1)
		}
	}
	os.WriteFile(VTTasksFile, []byte(strings.Join(lines, "\n")), 0644)
	fmt.Println("   ✅ TASKS.md 已更新为 ✅")
}

func updateSessionState(taskID string) {
	if !fileExists(VTSessionFile) {
		fmt.Println("   ⚠️  SESSION-STATE.md 不存在")
		return
	}

	content, _ := readFile(VTSessionFile)
	today := time.Now().Format("2006-01-02")

	replacements := map[string]string{
		"**会话日期** | YYYY-MM-DD":  fmt.Sprintf("**会话日期** | %s", today),
		"**执行任务** | T-XXX":       fmt.Sprintf("**执行任务** | %s", taskID),
		"**完成状态** | 🔄":          "**完成状态** | ✅",
		"**会话摘要** | [本次会话做了什么]": fmt.Sprintf("**会话摘要** | 完成任务 %s", taskID),
	}

	for old, new := range replacements {
		content = strings.Replace(content, old, new, 1)
	}

	os.WriteFile(VTSessionFile, []byte(content), 0644)
	fmt.Println("   ✅ SESSION-STATE.md 已更新")
}

func updateRequirements(taskID string) {
	if !fileExists(VTTasksFile) || !fileExists(VTReqsFile) {
		return
	}

	content, _ := readFile(VTTasksFile)
	reqIDs := regexp.MustCompile(`REQ-\d+`).FindAllString(content, -1)
	if len(reqIDs) == 0 {
		return
	}

	reqContent, _ := readFile(VTReqsFile)
	for _, req := range reqIDs {
		fmt.Printf("  → 更新需求 %s 状态\n", req)
		lines := strings.Split(reqContent, "\n")
		for i, line := range lines {
			if strings.Contains(line, req) && strings.Contains(line, "⬜") {
				lines[i] = strings.Replace(line, "⬜", "✅ 设计", 1)
			}
		}
		reqContent = strings.Join(lines, "\n")
	}
	os.WriteFile(VTReqsFile, []byte(reqContent), 0644)
}

func getChangedFiles() []string {
	cmd := exec.Command("git", "diff", "--name-only", "HEAD")
	out, err := cmd.Output()
	if err != nil {
		return nil
	}
	return strings.Split(strings.TrimSpace(string(out)), "\n")
}

func getChangedPackages(files []string, ext string) []string {
	pkgSet := make(map[string]bool)
	for _, f := range files {
		if strings.HasSuffix(f, ext) {
			dir := filepath.Dir(f)
			if dir != "" {
				pkgSet["./"+dir] = true
			}
		}
	}
	packages := make([]string, 0, len(pkgSet))
	for p := range pkgSet {
		packages = append(packages, p)
	}
	return packages
}

func filterByExt(files []string, ext string) []string {
	result := []string{}
	for _, f := range files {
		if strings.HasSuffix(f, ext) {
			result = append(result, f)
		}
	}
	return result
}

// checkTaskEvidence verifies the task's acceptance criteria (验收标准) in
// docs/tasks/T-XXX.md are all checked off. Returns false (blocking) if any
// remain unchecked — evidence-chain enforcement for the DoD.
func checkTaskEvidence(taskID string) bool {
	taskFile := taskDocPath(taskID)
	if !fileExists(taskFile) {
		fmt.Println("   ⚠️  无任务定义文件，跳过验收标准核销（建议创建 docs/tasks/T-XXX.md）")
		return true
	}
	data, _ := readFile(taskFile)

	unchecked, checked := 0, 0
	inAcceptance := false
	for _, line := range strings.Split(data, "\n") {
		trimmed := strings.TrimSpace(line)
		// Acceptance section starts at "验收标准" heading (H1 or H2+).
		if (strings.HasPrefix(trimmed, "#") && !strings.HasPrefix(trimmed, "###")) &&
			strings.Contains(trimmed, "验收标准") {
			inAcceptance = true
			continue
		}
		// A new ## section ends it; ### sub-headings (功能验收 etc.) do NOT.
		if inAcceptance && strings.HasPrefix(trimmed, "##") && !strings.HasPrefix(trimmed, "###") {
			inAcceptance = false
		}
		if !inAcceptance {
			continue
		}
		// Checkboxes, case-insensitive ("- [ ]", "- [x]", "- [X]").
		lower := strings.ToLower(trimmed)
		if strings.HasPrefix(lower, "- [ ]") {
			unchecked++
		}
		if strings.HasPrefix(lower, "- [x]") {
			checked++
		}
	}

	if unchecked > 0 {
		fmt.Printf("   ❌ 验收标准未全部核销：%d 项待勾选，%d 项已完成\n", unchecked, checked)
		return false
	}
	if checked == 0 && unchecked == 0 {
		fmt.Println("   ⚠️  验收标准段落无 checkbox 项，跳过")
		return true
	}
	fmt.Printf("   ✅ 验收标准全部核销（%d 项）\n", checked)
	return true
}

// checkMatrixCoverage warns when the functional test matrix still has empty
// cells (⬜), i.e. some function×dimension combination has no test yet.
// Returns true when an uncovered cell exists (testable without output capture).
func checkMatrixCoverage() bool {
	if !fileExists("docs/quality/TEST-MATRIX.md") {
		return false
	}
	data, _ := readFile("docs/quality/TEST-MATRIX.md")
	hasEmpty := strings.Contains(data, "⬜")
	if hasEmpty {
		fmt.Println("   ⚠️  TEST-MATRIX.md 存在未覆盖格子（⬜），确认对应功能已有测试")
	}
	return hasEmpty
}

func recordMetrics(taskID string, success bool) {
	metricsDir := ".claude/metrics"
	os.MkdirAll(metricsDir, 0755)

	metricsFile := filepath.Join(metricsDir, "verification.log")
	timestamp := time.Now().Format("2006-01-02T15:04:05")
	status := "PASS"
	if !success {
		status = "FAIL"
	}

	logEntry := fmt.Sprintf("%s|%s|%s|changed_only=%v\n", timestamp, taskID, status, vtChangedOnly)
	f, err := os.OpenFile(metricsFile, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return
	}
	defer f.Close()
	f.WriteString(logEntry)
	fmt.Printf("   📊 验收指标已记录到 %s\n", metricsFile)
}
