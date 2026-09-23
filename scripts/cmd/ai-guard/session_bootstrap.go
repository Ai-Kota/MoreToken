// session-bootstrap: SessionStart Hook
// Reads the 4 core project context files and writes a compact structured snapshot
// to .claude/context-snapshot.md so every session starts with the same context.
//
// Files read (in priority order):
//   1. CLAUDE.md              — behavior constitution
//   2. docs/tasks/TASKS.md    — task board (what to do)
//   3. docs/tasks/SESSION-STATE.md — last session (where we were)
//   4. INDEX.md               — navigation hub (where things are)
//
// Output: .claude/context-snapshot.md  (≤200 lines, structured)
package main

import (
	"fmt"
	"os"
	"strings"
)

const snapshotPath = ".claude/context-snapshot.md"

const snapshotHeader = `# 上下文快照（自动生成 · 每次会话注入）

> 本文件由 ai-guard session-bootstrap 在会话启动时生成。
> 作用：让 AI 在长会话 / 多会话中始终持有"项目状态 + 未完成项"的压缩视图。

---

## 当前任务状态（来自 TASKS.md）

| 状态 | 数量 |
|:----:|:----:|
| ✅ 已完成 | %d |
| 🔄 进行中 | %d |
| ⬜ 待办 | %d |
| ❌ 阻塞 | %d |

### 未完成任务（⬜ / 🔄 / ❌）

%s

## 上次会话（来自 SESSION-STATE.md）

%s

## 核心约定（来自 CLAUDE.md · 节选）

%s

---

## 强制约束

- **每个 commit ≤ %d 文件**（wu-guard hook 强制，超限需 WU-{Sprint}-{NN} 标记）
- **先读后写**（check-read-before-write hook 强制）
- **测试通过才算完成**（verify-task 强制）
- 任务状态同步到 TASKS.md，会话结束前更新 SESSION-STATE.md
`

func runSessionBootstrap(args []string) {
	// 1. Collect status counts + unfinished tasks from TASKS.md
	done, inProgress, todo, blocked, unfinished := scanTASKS()

	// 2. Extract last session summary from SESSION-STATE.md
	sessionSummary := extractSessionSummary()

	// 3. Extract core constraints from CLAUDE.md
	constraints := extractConstraints()

	// 4. Build the snapshot
	content := buildSnapshot(done, inProgress, todo, blocked, unfinished, sessionSummary, constraints)

	// 5. Write snapshot
	os.MkdirAll(".claude", 0755)
	if err := os.WriteFile(snapshotPath, []byte(content), 0644); err != nil {
		fmt.Fprintf(os.Stderr, "[session-bootstrap] WARN: cannot write %s: %v\n", snapshotPath, err)
		osExit(0)
	}

	// 6. Print pointer so the AI sees it in the hook output.
	fmt.Printf("[session-bootstrap] Context snapshot written: %s\n", snapshotPath)
}

// scanTASKS reads TASKS.md and counts task statuses + collects unfinished tasks.
func scanTASKS() (done, inProgress, todo, blocked int, unfinished []string) {
	data, err := os.ReadFile("docs/tasks/TASKS.md")
	if err != nil {
		return 0, 0, 0, 0, nil
	}

	lines := strings.Split(string(data), "\n")
	for _, line := range lines {
		// Match task rows: | T-XXX | desc | P0 | ⬜ | ...
		trimmed := strings.TrimSpace(line)
		if !strings.HasPrefix(trimmed, "| T-") {
			continue
		}
		// Extract the status column (4th field in a 7-column row).
		status := extractStatusColumn(trimmed)
		switch status {
		case "✅":
			done++
		case "🔄":
			inProgress++
			unfinished = append(unfinished, trimTaskRow(trimmed))
		case "⬜":
			todo++
			unfinished = append(unfinished, trimTaskRow(trimmed))
		case "❌":
			blocked++
			unfinished = append(unfinished, trimTaskRow(trimmed))
		}
	}
	return done, inProgress, todo, blocked, unfinished
}

// extractStatusColumn pulls the 4th column (status) from a TASKS.md table row.
func extractStatusColumn(row string) string {
	// Row shape: | T-XXX | task | P0 | ⬜ | ack | ...
	parts := strings.Split(row, "|")
	if len(parts) >= 5 {
		status := strings.TrimSpace(parts[4])
		// Strip markdown bold markers (e.g. **⬜**).
		status = strings.ReplaceAll(status, "**", "")
		return strings.TrimSpace(status)
	}
	return ""
}

// trimTaskRow reduces a TASKS.md row to "T-XXX | task | status".
func trimTaskRow(row string) string {
	parts := strings.Split(row, "|")
	if len(parts) < 5 {
		return row
	}
	// | T-001 | task | P0 | ⬜ | ...
	id := strings.TrimSpace(parts[1])
	task := strings.TrimSpace(parts[2])
	status := strings.TrimSpace(parts[4])
	return fmt.Sprintf("| %s | %s | %s |", id, truncateStr(task, 40), status)
}



// extractSessionSummary pulls the most recent "会话摘要" from SESSION-STATE.md.
func extractSessionSummary() string {
	data, err := os.ReadFile("docs/tasks/SESSION-STATE.md")
	if err != nil {
		return "（无 SESSION-STATE.md）"
	}
	lines := strings.Split(string(data), "\n")
	for _, line := range lines {
		if strings.Contains(line, "**会话摘要**") {
			// Row shape: | **会话摘要** | <text> |
			parts := strings.Split(line, "|")
			if len(parts) >= 3 {
				return strings.TrimSpace(parts[2])
			}
			return strings.TrimSpace(line)
		}
	}
	// Fallback: first non-empty line
	for _, line := range lines {
		if t := strings.TrimSpace(line); t != "" && !strings.HasPrefix(t, "#") {
			return truncateStr(t, 200)
		}
	}
	return "（无摘要）"
}

// extractConstraints pulls key behavioral rules from CLAUDE.md.
func extractConstraints() string {
	data, err := os.ReadFile("CLAUDE.md")
	if err != nil {
		return "（无 CLAUDE.md）"
	}

	var rules []string
	seen := map[string]bool{}
	// Priority subjects — sample a spread of rule categories.
	rulePatterns := []string{
		"先读后写", "先测试后", "自主执行", "必须停下来", "禁止", "不看过程",
	}
	lines := strings.Split(string(data), "\n")
	for _, line := range lines {
		t := strings.TrimSpace(line)
		if t == "" || strings.HasPrefix(t, "#") || strings.HasPrefix(t, ">") {
			continue
		}
		for _, subject := range rulePatterns {
			if !strings.Contains(t, subject) {
				continue
			}
			// Clean the cell: strip markdown table pipes to avoid fragments.
			clean := strings.ReplaceAll(t, "|", "")
			clean = strings.TrimSpace(clean)
			// Dedupe and cap.
			key := truncateStr(clean, 60)
			if seen[key] {
				break
			}
			seen[key] = true
			rules = append(rules, "- "+truncateStr(clean, 90))
			break
		}
		if len(rules) >= 8 {
			break
		}
	}
	if len(rules) == 0 {
		return "（无可用约束）"
	}
	return strings.Join(rules, "\n")
}

func buildSnapshot(done, inProgress, todo, blocked int, unfinished []string, sessionSummary, constraints string) string {
	var sb strings.Builder

	unfinishedStr := "（无未完成任务）"
	if len(unfinished) > 0 {
		unfinishedStr = strings.Join(unfinished, "\n")
	}

	content := fmt.Sprintf(snapshotHeader, done, inProgress, todo, blocked, unfinishedStr, sessionSummary, constraints, maxFilesPerCommit)
	sb.WriteString(content)
	return sb.String()
}


