// check-scope: PreToolUse Hook
// Validates that file modifications stay within the task's allowed scope.
// Exit 0 = allow, exit 1 = block.
package main

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"spt/scripts/internal/tracker"
)

func runCheckScope(args []string) {
	input, err := readHookInput()
	if err != nil {
		// Fail-closed: cannot determine the write scope, so block.
		fmt.Fprintf(os.Stderr, "[check-scope] ❌ hook 输入解析失败，拒绝写入（fail-closed）\n")
		osExit(2)
	}
	filePath := extractFilePath(input)
	if filePath == "" {
		osExit(0)
	}
	osExit(enforceWriteScope(filePath))
}

// enforceWriteScope is the shared write-scope gate used by both check-scope
// (Write|Edit hooks) and check-bash-write (Bash hooks). Returns the exit code:
//  0 = allow, 1 = blocked by task boundary, 2 = blocked by the intent gate.
func enforceWriteScope(rawPath string) int {
	// Convert absolute paths to relative to match task boundary definitions.
	filePath := rawPath
	if filepath.IsAbs(filePath) {
		if cwd, err := os.Getwd(); err == nil {
			if rel, err := filepath.Rel(cwd, filePath); err == nil {
				filePath = rel
			}
		}
	}
	filePath = filepath.ToSlash(filepath.Clean(filePath))

	// Intent gate: without a valid declared task, project-file writes are
	// blocked. A stale task_id (definition file deleted) is cleared and treated
	// as no-task, so a leftover context can never silently bypass the gate.
	taskID := tracker.GetTaskID()
	if taskID == "" || !fileExists(taskDocPath(taskID)) {
		if taskID != "" {
			tracker.ClearTask()
		}
		if isBlockedPath(filePath) {
			fmt.Fprintf(os.Stderr, "[check-scope] ❌ 未声明任务，禁止写入 %s。\n", filePath)
			fmt.Fprintf(os.Stderr, "[check-scope] 请先运行: ai-guard begin-task T-XXX（读取任务定义文件并声明意图）\n")
			return 2
		}
		return 0
	}

	// Load task boundaries
	allowed, denied, newFileDirs := loadTaskBoundaries(taskID)

	// Check if file is in denied list
	if isDenied(filePath, denied) {
		fmt.Fprintf(os.Stderr, "ERROR: File %s is in the denied list for task %s.\n"+
			"       Denied files: %s\n"+
			"       Cannot modify these files.\n",
			filePath, taskID, strings.Join(denied, ", "))
		return 1
	}

	// Check if file is in allowed list (if specified)
	if len(allowed) > 0 && !isAllowed(filePath, allowed) {
		fmt.Fprintf(os.Stderr, "ERROR: File %s is not in the allowed list for task %s.\n"+
			"       Allowed files: %s\n"+
			"       Only these files can be modified.\n",
			filePath, taskID, strings.Join(allowed, ", "))
		return 1
	}

	// For new files, check if directory is allowed
	if !fileExists(filePath) && len(newFileDirs) > 0 {
		if !isNewFileAllowed(filePath, newFileDirs) {
			fmt.Fprintf(os.Stderr, "ERROR: Creating file %s is not allowed for task %s.\n"+
				"       Allowed new file directories: %s\n",
				filePath, taskID, strings.Join(newFileDirs, ", "))
			return 1
		}
	}

	return 0
}

func loadTaskBoundaries(taskID string) (allowed, denied, newFileDirs []string) {
	taskFile := taskDocPath(taskID)
	data, err := os.ReadFile(taskFile)
	if err != nil {
		return nil, nil, nil
	}

	content := string(data)
	lines := strings.Split(content, "\n")

	for _, line := range lines {
		if strings.Contains(line, "允许修改文件") {
			parts := strings.SplitN(line, "：", 2)
			if len(parts) < 2 {
				parts = strings.SplitN(line, ":", 2)
			}
			if len(parts) >= 2 {
				allowed = parseFileList(parts[1])
			}
		}
		if strings.Contains(line, "禁止修改文件") {
			parts := strings.SplitN(line, "：", 2)
			if len(parts) < 2 {
				parts = strings.SplitN(line, ":", 2)
			}
			if len(parts) >= 2 {
				denied = parseFileList(parts[1])
			}
		}
		if strings.Contains(line, "允许新增文件目录") {
			parts := strings.SplitN(line, "：", 2)
			if len(parts) < 2 {
				parts = strings.SplitN(line, ":", 2)
			}
			if len(parts) >= 2 {
				newFileDirs = parseFileList(parts[1])
			}
		}
	}

	return allowed, denied, newFileDirs
}

func parseFileList(s string) []string {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil
	}
	result := strings.Split(s, ",")
	for i := range result {
		result[i] = strings.TrimSpace(result[i])
		result[i] = filepath.ToSlash(result[i])
	}
	return result
}

func isDenied(filePath string, denied []string) bool {
	for _, d := range denied {
		if matchPath(filePath, d) {
			return true
		}
	}
	return false
}

func isAllowed(filePath string, allowed []string) bool {
	for _, a := range allowed {
		if matchPath(filePath, a) {
			return true
		}
	}
	return false
}

func isNewFileAllowed(filePath string, dirs []string) bool {
	for _, d := range dirs {
		d = strings.TrimRight(d, "/") + "/"
		if strings.HasPrefix(filePath, d) {
			return true
		}
	}
	return false
}

func matchPath(filePath, pattern string) bool {
	if filePath == pattern {
		return true
	}
	if strings.HasSuffix(pattern, "/") {
		return strings.HasPrefix(filePath, pattern)
	}
	matched, _ := filepath.Match(pattern, filePath)
	return matched
}

// isBlockedPath reports whether a project-file write is blocked when no valid
// task is declared. Exemptions:
//   - .claude/ internal state (tracker/metrics) so the hooks can manage themselves;
//   - task definition files docs/tasks/T-*.md (planning layer) — the intent gate
//     must not block creating/maintaining the very task files that unlock it;
//   - an initialization window: while the project has no task definitions yet
//     (fresh template), scaffolding docs/ and root markdown is allowed so INIT
//     can bootstrap the task board. Once a task exists, enforcement is strict.
func isBlockedPath(filePath string) bool {
	if strings.HasPrefix(filePath, ".claude/") {
		return false
	}
	if isTaskDefinitionPath(filePath) {
		return false
	}
	if !hasTaskDefinitions() {
		if strings.HasPrefix(filePath, "docs/") || isRootMarkdown(filePath) {
			return false
		}
	}
	return true
}

// isTaskDefinitionPath reports whether p is a per-task definition file
// (docs/tasks/T-*.md) or the task board index (docs/tasks/TASKS.md).
//
// TASKS.md 列入其中：begin-task 靠它的任务行校验任务 ID，缺它则门禁死锁
// （登记新任务需要已声明的任务，而声明任务需要已登记的任务行）。
// 本改动只放宽"无任务"窗口——任务一旦声明，isBlockedPath 即不可达，
// TASKS.md 照常受任务边界（loadTaskBoundaries / isAllowed）约束。
func isTaskDefinitionPath(p string) bool {
	base := filepath.Base(p)
	return strings.HasPrefix(p, "docs/tasks/") && strings.HasSuffix(p, ".md") &&
		(strings.HasPrefix(base, "T-") || base == "TASKS.md")
}

// hasTaskDefinitions reports whether docs/tasks/TASKS.md contains any task row.
// (?m) is required so ^ matches at the start of each line — a real TASKS.md
// starts with a "# 任务清单" heading, not a task row.
func hasTaskDefinitions() bool {
	data, err := os.ReadFile("docs/tasks/TASKS.md")
	if err != nil {
		return false
	}
	return regexp.MustCompile(`(?m)^\|\s*T-\d+`).MatchString(string(data))
}

// isRootMarkdown reports whether p is a markdown file at the repo root.
func isRootMarkdown(p string) bool {
	return strings.HasSuffix(p, ".md") && !strings.Contains(p, "/")
}
