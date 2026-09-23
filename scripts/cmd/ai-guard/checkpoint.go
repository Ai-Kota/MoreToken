// checkpoint: Auto-save development progress to SESSION-STATE.md
// Records current task status, changed files, and next steps.
// Usage: checkpoint [task-id] [--summary "what was done"]
package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"spt/scripts/internal/tracker"
)

const SessionFile = "docs/tasks/SESSION-STATE.md"

type CheckpointData struct {
	TaskID      string
	Summary     string
	ChangedFiles []string
	Timestamp   string
	Phase       string
	NextSteps   string
}

func runCheckpoint(args []string) {
	data := CheckpointData{
		Timestamp: time.Now().Format("2006-01-02 15:04:05"),
	}

	// Whether to refresh the context snapshot for the next session.
	injectNextSession := true

	// Parse args
	for i, arg := range args {
		switch arg {
		case "--summary":
			if i+1 < len(args) {
				data.Summary = args[i+1]
			}
		case "--next":
			if i+1 < len(args) {
				data.NextSteps = args[i+1]
			}
		case "--phase":
			if i+1 < len(args) {
				data.Phase = args[i+1]
			}
		case "--no-inject":
			injectNextSession = false
		default:
			// If it looks like a task ID
			if matched, _ := regexp.MatchString(`^T-\d+`, arg); matched {
				data.TaskID = arg
			}
		}
	}

	// If no task ID, try to detect from tracker
	if data.TaskID == "" {
		data.TaskID = tracker.GetTaskID()
	}

	// Get changed files
	data.ChangedFiles = getCheckpointChangedFiles()

	// Build checkpoint content
	content := buildCheckpointContent(data)

	// Write to SESSION-STATE.md
	os.MkdirAll(filepath.Dir(SessionFile), 0755)
	if err := os.WriteFile(SessionFile, []byte(content), 0644); err != nil {
		fmt.Fprintf(os.Stderr, "❌ Failed to write checkpoint: %v\n", err)
		osExit(1)
	}

	fmt.Printf("✅ Checkpoint saved: %s\n", data.Timestamp)
	if data.TaskID != "" {
		fmt.Printf("   Task: %s\n", data.TaskID)
	}
	if data.Summary != "" {
		fmt.Printf("   Summary: %s\n", data.Summary)
	}
	fmt.Printf("   Changed files: %d\n", len(data.ChangedFiles))
	fmt.Printf("   → %s\n", SessionFile)

	// Refresh the context snapshot so the next session starts with fresh state.
	if injectNextSession {
		runSessionBootstrap(nil)
	}
}

func getCheckpointChangedFiles() []string {
	cmd := exec.Command("git", "diff", "--name-only", "HEAD")
	out, err := cmd.Output()
	if err != nil {
		return nil
	}
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	result := []string{}
	for _, l := range lines {
		l = strings.TrimSpace(l)
		if l != "" {
			result = append(result, l)
		}
	}
	return result
}

func buildCheckpointContent(data CheckpointData) string {
	var sb strings.Builder

	sb.WriteString("# 会话状态（跨会话上下文同步）\n\n")
	sb.WriteString("> **本文件由 ai-guard checkpoint 自动生成。**\n")
	sb.WriteString("> AI 每次会话开始时首先读取此文件，从上次中断处继续。\n\n")
	sb.WriteString("---\n\n")

	// Last session info
	sb.WriteString("## 最后一次会话\n\n")
	sb.WriteString("| 字段 | 值 |\n")
	sb.WriteString("|------|------|\n")
	sb.WriteString(fmt.Sprintf("| **会话日期** | %s |\n", data.Timestamp))
	if data.TaskID != "" {
		sb.WriteString(fmt.Sprintf("| **执行任务** | %s |\n", data.TaskID))
	} else {
		sb.WriteString("| **执行任务** | 无 |\n")
	}
	sb.WriteString("| **完成状态** | 🔄 进行中 |\n")
	if data.Summary != "" {
		sb.WriteString(fmt.Sprintf("| **会话摘要** | %s |\n", data.Summary))
	} else {
		sb.WriteString("| **会话摘要** | （待补充） |\n")
	}
	sb.WriteString("\n---\n\n")

	// Current work snapshot
	sb.WriteString("## 当前工作快照\n\n")
	sb.WriteString("> AI 下次会话从这里恢复。\n\n")

	// Current task
	sb.WriteString("### 正在做的任务\n\n")
	sb.WriteString("| 任务ID | 进度 | 停在哪里 | 下一步 |\n")
	sb.WriteString("|:------:|:----:|---------|--------|\n")
	if data.TaskID != "" {
		if data.NextSteps != "" {
			sb.WriteString(fmt.Sprintf("| %s | 🔄 | 上次会话中断 | %s |\n", data.TaskID, data.NextSteps))
		} else {
			sb.WriteString(fmt.Sprintf("| %s | 🔄 | 上次会话中断 | 继续执行 |\n", data.TaskID))
		}
	} else {
		sb.WriteString("| 无 | — | — | 等待用户分配任务 |\n")
	}
	sb.WriteString("\n")

	// Changed files
	sb.WriteString("### 未完成的修改\n\n")
	if len(data.ChangedFiles) > 0 {
		sb.WriteString("| 文件 | 修改内容 | 是否已提交 |\n")
		sb.WriteString("|------|---------|:---------:|\n")
		for _, f := range data.ChangedFiles {
			sb.WriteString(fmt.Sprintf("| %s | （待确认） | ⬜ |\n", f))
		}
	} else {
		sb.WriteString("| 无变更文件 | — | — |\n")
	}
	sb.WriteString("\n")

	// Decision items (empty for now)
	sb.WriteString("### 待决策项\n\n")
	sb.WriteString("| # | 问题 | 建议方案A | 建议方案B | 决策 |\n")
	sb.WriteString("|---|------|----------|----------|------|\n")
	sb.WriteString("| | | | | ⬜ |\n")
	sb.WriteString("\n---\n\n")

	// Blockers
	sb.WriteString("## 阻塞项\n\n")
	sb.WriteString("| 阻塞ID | 任务 | 原因 | 需要谁 | 状态 |\n")
	sb.WriteString("|:------:|------|------|--------|:----:|\n")
	sb.WriteString("| | | | | ⬜ |\n")
	sb.WriteString("\n---\n\n")

	// Context summary
	sb.WriteString("## 上下文摘要\n\n")
	sb.WriteString("> AI 在此记录对下次会话有用的上下文信息。\n\n")

	sb.WriteString("### 项目当前状态\n\n")
	if data.Summary != "" {
		sb.WriteString(fmt.Sprintf("%s\n\n", data.Summary))
	} else {
		sb.WriteString("（等待 AI 补充）\n\n")
	}

	sb.WriteString("### 关键约定\n\n")
	sb.WriteString("- （等待 AI 补充）\n\n")

	sb.WriteString("### 环境状态\n\n")
	sb.WriteString("- （等待 AI 补充）\n\n")

	sb.WriteString("---\n\n")

	// Session history
	sb.WriteString("## 会话历史\n\n")
	sb.WriteString("| 日期 | 任务 | 成果 | 问题 |\n")
	sb.WriteString("|------|------|------|------|\n")
	sb.WriteString(fmt.Sprintf("| %s | %s | %s | |\n",
		data.Timestamp[:10],
		data.TaskID,
		truncateCheckpointStr(data.Summary, 30),
	))

	return sb.String()
}

func truncateCheckpointStr(s string, maxLen int) string {
	if s == "" {
		return "—"
	}
	if maxLen <= 0 {
		return ""
	}
	if len(s) <= maxLen {
		return s
	}
	if maxLen <= 3 {
		return s[:maxLen]
	}
	return s[:maxLen-3] + "..."
}
