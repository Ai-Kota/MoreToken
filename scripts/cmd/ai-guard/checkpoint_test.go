package main

import (
	"strings"
	"testing"
)

func TestBuildCheckpointContent(t *testing.T) {
	data := CheckpointData{
		TaskID:       "T-201",
		Summary:      "Sprint 1 完成",
		ChangedFiles: []string{"src/a.go", "src/b.go"},
		Timestamp:    "2026-08-26 13:16:31",
		Phase:        "Phase 2",
		NextSteps:    "开始 Sprint 2",
	}

	content := buildCheckpointContent(data)

	for _, want := range []string{
		"# 会话状态（跨会话上下文同步）",
		"| **执行任务** | T-201 |",
		"| **会话摘要** | Sprint 1 完成 |",
		"| T-201 | 🔄 | 上次会话中断 | 开始 Sprint 2 |",
		"| src/a.go | （待确认） | ⬜ |",
		"| src/b.go | （待确认） | ⬜ |",
		"| 2026-08-26 | T-201 | Sprint 1 完成 | |",
	} {
		if !strings.Contains(content, want) {
			t.Errorf("checkpoint content missing %q\ncontent:\n%s", want, content)
		}
	}
}

func TestBuildCheckpointContentEmpty(t *testing.T) {
	content := buildCheckpointContent(CheckpointData{Timestamp: "2026-08-26 00:00:00"})

	for _, want := range []string{
		"| **执行任务** | 无 |",
		"| **会话摘要** | （待补充） |",
		"| 无 | — | — | 等待用户分配任务 |",
		"| 无变更文件 | — | — |",
	} {
		if !strings.Contains(content, want) {
			t.Errorf("empty checkpoint content missing %q", want)
		}
	}
}

func TestTruncateCheckpointStr(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		maxLen   int
		expected string
	}{
		{"empty", "", 10, "—"},
		{"short", "abc", 10, "abc"},
		{"exact", "abcdefghij", 10, "abcdefghij"},
		{"truncated", "abcdefghijklm", 10, "abcdefg..."},
		{"zero-max", "abc", 0, ""},
		{"max-3", "abcdef", 3, "abc"},
		{"negative-max", "abc", -1, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := truncateCheckpointStr(tt.input, tt.maxLen); got != tt.expected {
				t.Errorf("truncateCheckpointStr(%q, %d) = %q, want %q", tt.input, tt.maxLen, got, tt.expected)
			}
		})
	}
}
