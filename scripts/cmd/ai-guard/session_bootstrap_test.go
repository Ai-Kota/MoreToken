package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeRelFile writes a file under a temp dir, creating parent dirs.
func writeRelFile(t *testing.T, dir, rel, content string) {
	t.Helper()
	full := filepath.Join(dir, rel)
	if err := os.MkdirAll(filepath.Dir(full), 0755); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(full), err)
	}
	if err := os.WriteFile(full, []byte(content), 0644); err != nil {
		t.Fatalf("write %s: %v", full, err)
	}
}

func TestScanTASKS(t *testing.T) {
	tmpDir := t.TempDir()
	origWd, _ := os.Getwd()
	os.Chdir(tmpDir)
	defer os.Chdir(origWd)

	writeRelFile(t, tmpDir, "docs/tasks/TASKS.md", `# 任务清单
| ID | Task | P | 状态 | 备注 |
| T-001 | init | P0 | ✅ | |
| T-002 | build | P0 | 🔄 | |
| T-003 | test | P0 | ⬜ | |
| T-004 | deploy | P0 | ❌ | |`)

	done, inProgress, todo, blocked, unfinished := scanTASKS()
	if done != 1 || inProgress != 1 || todo != 1 || blocked != 1 {
		t.Errorf("counts = done:%d inProg:%d todo:%d blocked:%d, want all 1", done, inProgress, todo, blocked)
	}
	if len(unfinished) != 3 {
		t.Errorf("unfinished = %d, want 3 (got %v)", len(unfinished), unfinished)
	}
	// Unfinished rows should carry the T-XXX prefix and status.
	if len(unfinished) > 0 && !strings.HasPrefix(unfinished[0], "| T-") {
		t.Errorf("unfinished row malformed: %q", unfinished[0])
	}
}

func TestScanTASKSMissing(t *testing.T) {
	tmpDir := t.TempDir()
	origWd, _ := os.Getwd()
	os.Chdir(tmpDir)
	defer os.Chdir(origWd)

	done, inProgress, todo, blocked, unfinished := scanTASKS()
	if done != 0 || inProgress != 0 || todo != 0 || blocked != 0 || len(unfinished) != 0 {
		t.Errorf("missing TASKS.md should return zeros, got %d/%d/%d/%d %v", done, inProgress, todo, blocked, unfinished)
	}
}

func TestExtractStatusColumn(t *testing.T) {
	tests := []struct {
		name  string
		row   string
		want  string
	}{
		{"plain", "| T-001 | init | P0 | ✅ | |", "✅"},
		{"bold", "| T-002 | init | P0 | **⬜** | |", "⬜"},
		{"too-short", "| T-001 | init", ""},
		{"empty-status", "| T-001 | init | P0 | | |", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := extractStatusColumn(tt.row); got != tt.want {
				t.Errorf("extractStatusColumn(%q) = %q, want %q", tt.row, got, tt.want)
			}
		})
	}
}

func TestTrimTaskRow(t *testing.T) {
	row := "| T-101 | 完成系统架构设计 | P0 | ✅ | T-004 |"
	got := trimTaskRow(row)
	if !strings.Contains(got, "T-101") || !strings.Contains(got, "✅") {
		t.Errorf("trimTaskRow = %q, want T-101 + status", got)
	}
	// Short rows pass through unchanged.
	if got := trimTaskRow("short"); got != "short" {
		t.Errorf("short row should pass through, got %q", got)
	}
}

func TestExtractSessionSummary(t *testing.T) {
	tmpDir := t.TempDir()
	origWd, _ := os.Getwd()
	os.Chdir(tmpDir)
	defer os.Chdir(origWd)

	writeRelFile(t, tmpDir, "docs/tasks/SESSION-STATE.md", `# 会话状态
| **会话摘要** | Sprint 1 完成 |
| **执行任务** | T-201 |`)

	if got := extractSessionSummary(); got != "Sprint 1 完成" {
		t.Errorf("extractSessionSummary = %q, want %q", got, "Sprint 1 完成")
	}
}

func TestExtractSessionSummaryMissing(t *testing.T) {
	tmpDir := t.TempDir()
	origWd, _ := os.Getwd()
	os.Chdir(tmpDir)
	defer os.Chdir(origWd)

	if got := extractSessionSummary(); got != "（无 SESSION-STATE.md）" {
		t.Errorf("missing file = %q, want fallback", got)
	}
}

func TestExtractSessionSummaryFallback(t *testing.T) {
	tmpDir := t.TempDir()
	origWd, _ := os.Getwd()
	os.Chdir(tmpDir)
	defer os.Chdir(origWd)

	// No 会话摘要 row → first non-heading line.
	writeRelFile(t, tmpDir, "docs/tasks/SESSION-STATE.md", "# Title\n\nSome plain text here\n")
	if got := extractSessionSummary(); got != "Some plain text here" {
		t.Errorf("fallback = %q, want first plain line", got)
	}
}

func TestExtractConstraints(t *testing.T) {
	tmpDir := t.TempDir()
	origWd, _ := os.Getwd()
	os.Chdir(tmpDir)
	defer os.Chdir(origWd)

	writeRelFile(t, tmpDir, "CLAUDE.md", `# 行为宪法

| 规则 | 说明 |
|------|------|
| 先读后写 | 修改前必须先 Read |
| 禁止跳过测试 | 质量失控 |
| 自主执行 | 直接编码不问 |

代码风格：简洁`)

	rules := extractConstraints()
	if !strings.Contains(rules, "先读后写") {
		t.Errorf("extractConstraints missing 先读后写, got:\n%s", rules)
	}
	if !strings.Contains(rules, "禁止") {
		t.Errorf("extractConstraints missing 禁止 rule, got:\n%s", rules)
	}
}

func TestExtractConstraintsMissing(t *testing.T) {
	tmpDir := t.TempDir()
	origWd, _ := os.Getwd()
	os.Chdir(tmpDir)
	defer os.Chdir(origWd)

	if got := extractConstraints(); got != "（无 CLAUDE.md）" {
		t.Errorf("missing CLAUDE.md = %q", got)
	}
}

func TestBuildSnapshot(t *testing.T) {
	content := buildSnapshot(3, 1, 2, 0, []string{"| T-002 | build | 🔄 |", "| T-003 | test | ⬜ |"}, "上次在做 Sprint 1", "- 禁止\n- 先测试后完成")

	for _, want := range []string{
		"| ✅ 已完成 | 3 |",
		"| 🔄 进行中 | 1 |",
		"| ⬜ 待办 | 2 |",
		"| T-002 | build | 🔄 |",
		"上次在做 Sprint 1",
		"- 禁止",
	} {
		if !strings.Contains(content, want) {
			t.Errorf("snapshot missing %q\ncontent:\n%s", want, content)
		}
	}
}

func TestBuildSnapshotNoUnfinished(t *testing.T) {
	content := buildSnapshot(0, 0, 0, 0, nil, "", "")
	if !strings.Contains(content, "（无未完成任务）") {
		t.Errorf("snapshot should show no-unfinished placeholder, got:\n%s", content)
	}
}
