// pre-task-check: Task start gate
// Validates all preconditions before starting a task.
// Usage: pre-task-check T-XXX
package main

import (
	"fmt"
	"path/filepath"
	"regexp"
	"strings"
)

const (
	PTCTasksFile      = "docs/tasks/TASKS.md"
	PTCGoalsFile      = "docs/project/GOALS.md"
	PTCArchFile       = "docs/architecture/ARCHITECTURE.md"
	PTCConstraintsDir = "docs/constraints"
)

func runPreTaskCheck(args []string) {
	if len(args) < 1 {
		fmt.Println("Usage: ai-guard pre-task-check T-XXX")
		osExit(1)
	}

	taskID := args[0]
	errors := []string{}
	warnings := []string{}

	fmt.Println("==================================================")
	fmt.Printf("任务开始前置检查: %s\n", taskID)
	fmt.Println("==================================================")

	// 1. Check task exists
	fmt.Println("\n1️⃣ 检查任务存在性...")
	if !fileExists(PTCTasksFile) {
		fmt.Println("   ❌ TASKS.md 不存在")
		osExit(1)
	}
	content, _ := readFile(PTCTasksFile)
	if !strings.Contains(content, taskID) {
		fmt.Printf("   ❌ 任务 %s 不存在于 TASKS.md\n", taskID)
		osExit(1)
	}
	fmt.Printf("   ✅ 任务 %s 存在\n", taskID)

	// 2. Check task status
	fmt.Println("2️⃣ 检查任务状态...")
	taskLine := findTaskLine(content, taskID)
	if strings.Contains(taskLine, "⬜") {
		fmt.Println("   ✅ 任务未开始，可以启动")
	} else if strings.Contains(taskLine, "🔄") {
		fmt.Println("   ✅ 已进行中，可以继续")
	} else if strings.Contains(taskLine, "✅") {
		fmt.Println("   ⚠️  已标记完成")
	} else if strings.Contains(taskLine, "❌") {
		fmt.Println("   ⚠️  已阻塞，需要解除")
	}

	// 3. Check prerequisites
	fmt.Println("3️⃣ 检查前置任务...")
	if ok, msg := checkPrerequisites(content, taskID); ok {
		fmt.Println("   ✅ 前置条件满足")
	} else {
		fmt.Printf("   ❌ %s\n", msg)
		errors = append(errors, msg)
	}

	// 4. Check design docs
	fmt.Println("4️⃣ 检查设计文档...")
	if !fileExists(PTCGoalsFile) {
		warnings = append(warnings, fmt.Sprintf("缺失: %s", PTCGoalsFile))
		fmt.Printf("   ⚠️  缺失: %s\n", PTCGoalsFile)
	}
	if !fileExists(PTCArchFile) {
		warnings = append(warnings, fmt.Sprintf("缺失: %s", PTCArchFile))
		fmt.Printf("   ⚠️  缺失: %s\n", PTCArchFile)
	}
	modsDir := "docs/architecture/modules"
	if dirExists(modsDir) {
		mds, _ := filepath.Glob(filepath.Join(modsDir, "*.md"))
		if len(mds) == 0 {
			warnings = append(warnings, fmt.Sprintf("模块设计目录为空: %s", modsDir))
			fmt.Printf("   ⚠️  %s\n", warnings[len(warnings)-1])
		}
	}

	// 5. Check constraint docs
	fmt.Println("5️⃣ 检查约束文档...")
	requiredConstraints := []string{
		"docs/constraints/ANTI-HALLUCINATION.md",
		"docs/constraints/AUTHORIZATION-MODEL.md",
	}
	for _, c := range requiredConstraints {
		if !fileExists(c) {
			fmt.Printf("   ❌ 缺失: %s\n", c)
			errors = append(errors, fmt.Sprintf("缺失约束文档: %s", c))
		}
	}
	if len(errors) == 0 {
		fmt.Println("   ✅ 约束文档完整")
	}

	// 6. Check session state
	fmt.Println("6️⃣ 检查会话状态...")
	sessionFile := "docs/tasks/SESSION-STATE.md"
	if fileExists(sessionFile) {
		fmt.Println("   ✅ SESSION-STATE.md 存在")
	} else {
		fmt.Printf("   ⚠️  %s 不存在\n", sessionFile)
		warnings = append(warnings, fmt.Sprintf("%s 不存在", sessionFile))
	}

	// 7. Check task definition file
	fmt.Println("7️⃣ 检查任务定义文件...")
	taskFile := fmt.Sprintf("docs/tasks/T-%s.md", taskID)
	if fileExists(taskFile) {
		taskContent, _ := readFile(taskFile)
		if strings.Contains(taskContent, "允许修改文件") && strings.Contains(taskContent, "**允许修改文件**：") {
			fmt.Println("   ⚠️  任务边界未填写（允许修改文件）")
			warnings = append(warnings, "任务边界未填写")
		} else {
			fmt.Println("   ✅ 任务定义完整")
		}
	} else {
		fmt.Printf("   ⚠️  可选: 任务定义文件不存在 %s\n", taskFile)
	}

	// Result
	fmt.Println("\n==================================================")
	if len(errors) == 0 {
		fmt.Printf("✅ 任务 %s 前置检查通过，可以开始执行\n", taskID)
		osExit(0)
	} else {
		fmt.Printf("❌ 任务 %s 前置检查失败\n", taskID)
		fmt.Printf("   错误: %d 个\n", len(errors))
		fmt.Printf("   警告: %d 个\n", len(warnings))
		osExit(1)
	}
}

// isTaskRowFor reports whether a TASKS.md row's ID cell equals taskID.
// Matches the ID column exactly instead of substring-contains, so a task ID
// appearing in another row's dependency column is not misread as that row.
func isTaskRowFor(line, taskID string) bool {
	parts := strings.Split(line, "|")
	if len(parts) < 2 {
		return false
	}
	return strings.TrimSpace(parts[1]) == taskID
}

func checkPrerequisites(content, taskID string) (bool, string) {
	lines := strings.Split(content, "\n")
	for _, line := range lines {
		if isTaskRowFor(line, taskID) {
			parts := strings.Split(line, "|")
			if len(parts) > 4 {
				deps := strings.TrimSpace(parts[4])
				if deps == "" || deps == "—" {
					return true, ""
				}
				depIDs := regexp.MustCompile(`T-\d+`).FindAllString(deps, -1)
				for _, depID := range depIDs {
					depLine := findTaskLine(content, depID)
					if !strings.Contains(depLine, "✅") {
						return false, fmt.Sprintf("前置任务 %s 未完成", depID)
					}
				}
			}
		}
	}
	return true, ""
}
