// begin-task: declare the active task context (intent gate).
// Usage: begin-task T-XXX
//
// Loads the task's boundaries (允许/禁止修改文件, 允许新增文件目录) and
// acceptance criteria (验收标准) from docs/tasks/T-XXX.md into the tracker.
// PreToolUse hooks (check-scope) then enforce that every write stays within
// the declared task. Without a declared task, check-scope blocks writes.
package main

import (
	"fmt"
	"os"
	"strings"

	"spt/scripts/internal/tracker"
)

func runBeginTask(args []string) {
	if len(args) < 1 {
		fmt.Println("Usage: ai-guard begin-task T-XXX")
		osExit(1)
	}
	taskID := args[0]

	if !fileExists(PTCTasksFile) {
		fmt.Println("❌ TASKS.md 不存在")
		osExit(1)
	}
	content, _ := readFile(PTCTasksFile)
	found := false
	for _, line := range strings.Split(content, "\n") {
		if isTaskRowFor(line, taskID) {
			found = true
			break
		}
	}
	if !found {
		fmt.Printf("❌ 任务 %s 不存在于 TASKS.md（任务 ID 需精确匹配任务行）\n", taskID)
		osExit(1)
	}

	allowed, denied, newDirs := loadTaskBoundaries(taskID)
	if len(allowed) == 0 {
		fmt.Printf("❌ 任务 %s 未定义「允许修改文件」，check-scope 将无法约束写入（fail-open 风险）。\n", taskID)
		fmt.Println("   请在 docs/tasks/T-XXX.md 填写任务边界后重试")
		osExit(1)
	}
	dod := extractDoD(taskID)
	tracker.BeginTask(taskID, allowed, denied, newDirs, dod)

	fmt.Printf("✅ 任务 %s 已声明（begin-task）\n", taskID)
	fmt.Printf("   允许修改文件: %v\n", allowed)
	fmt.Printf("   禁止修改文件: %v\n", denied)
	fmt.Printf("   允许新增目录: %v\n", newDirs)
	fmt.Printf("   验收标准(%d 项): %v\n", len(dod), dod)
	fmt.Println()
	fmt.Println("现在可在此任务范围内写文件。范围外的写入将被 check-scope 拦截。")
}

// runEndTask clears the active task context (intent gate teardown). Use when
// a task finishes or when starting an unrelated piece of work.
func runEndTask(args []string) {
	tracker.ClearTask()
	fmt.Println("✅ 任务上下文已清除（end-task）")
}

// extractDoD pulls the acceptance-criteria checkbox items ("- [ ] ACx: ...")
// from the task definition file docs/tasks/T-XXX.md.
func extractDoD(taskID string) []string {
	taskFile := taskDocPath(taskID)
	data, err := os.ReadFile(taskFile)
	if err != nil {
		return nil
	}

	var dod []string
	inAcceptance := false
	for _, line := range strings.Split(string(data), "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "#") {
			isSub := strings.HasPrefix(trimmed, "###")
			if strings.Contains(trimmed, "验收标准") {
				inAcceptance = true
				continue
			}
			// ### sub-headings (功能验收 / 技术验收) don't end the section.
			if inAcceptance && !isSub && len(strings.Trim(strings.TrimPrefix(trimmed, "#"), " #")) > 0 {
				inAcceptance = false
			}
			continue
		}
		if !inAcceptance {
			continue
		}
		if strings.HasPrefix(trimmed, "- [") {
			if idx := strings.Index(trimmed, "] "); idx >= 0 {
				item := strings.TrimSpace(trimmed[idx+2:])
				if item != "" {
					dod = append(dod, item)
				}
			}
		}
	}
	return dod
}
