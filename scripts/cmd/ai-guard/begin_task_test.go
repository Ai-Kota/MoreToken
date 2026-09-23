package main

import (
	"testing"

	"spt/scripts/internal/tracker"
)

func TestRunBeginTask(t *testing.T) {
	dir := chdirTemp(t)
	writeRelFile(t, dir, "docs/tasks/TASKS.md", `| T-101 | init | P0 | — | ⬜ |`)
	writeRelFile(t, dir, "docs/tasks/T-101.md", `# 任务 T-101
**允许修改文件**：src/a.go
**禁止修改文件**：deploy/

## 验收标准
- [ ] AC1: 用户能登录
- [ ] AC2: 用户能退出
`)

	code := runWithExitCapture(t, func() { runBeginTask([]string{"T-101"}) })
	if code == 1 {
		t.Errorf("begin-task should not fail, got exit %d", code)
	}

	ctx := tracker.GetTaskContext()
	if ctx.TaskID != "T-101" {
		t.Errorf("task context = %q, want T-101", ctx.TaskID)
	}
	if len(ctx.AllowedFiles) != 1 || ctx.AllowedFiles[0] != "src/a.go" {
		t.Errorf("allowed files = %v", ctx.AllowedFiles)
	}
	if len(ctx.DeniedFiles) != 1 || ctx.DeniedFiles[0] != "deploy/" {
		t.Errorf("denied files = %v", ctx.DeniedFiles)
	}
	if len(ctx.DoD) != 2 {
		t.Errorf("DoD = %v, want 2 items", ctx.DoD)
	}
	if ctx.TaskStartedAt == "" {
		t.Error("task_started_at should be set")
	}
}

func TestRunBeginTaskMissingTask(t *testing.T) {
	dir := chdirTemp(t)
	writeRelFile(t, dir, "docs/tasks/TASKS.md", `| T-101 | init | P0 | — | ⬜ |`)

	code := runWithExitCapture(t, func() { runBeginTask([]string{"T-999"}) })
	if code != 1 {
		t.Errorf("begin-task on missing task should exit 1, got %d", code)
	}
}

func TestRunBeginTaskNoArgs(t *testing.T) {
	chdirTemp(t)
	code := runWithExitCapture(t, func() { runBeginTask(nil) })
	if code != 1 {
		t.Errorf("begin-task without args should exit 1, got %d", code)
	}
}

func TestExtractDoD(t *testing.T) {
	dir := chdirTemp(t)
	writeRelFile(t, dir, "docs/tasks/T-101.md", `# 任务
## 验收标准
- [ ] AC1: 能登录
- [x] AC2: 能退出
## 其他事项
- [ ] 不是验收标准里的项
`)
	dod := extractDoD("T-101")
	if len(dod) != 2 {
		t.Errorf("extractDoD = %v, want only the 2 acceptance items", dod)
	}
	for _, item := range dod {
		if item == "不是验收标准里的项" {
			t.Errorf("should not include items outside 验收标准 section: %v", dod)
		}
	}
}

func TestExtractDoDMissingFile(t *testing.T) {
	chdirTemp(t)
	if dod := extractDoD("T-999"); len(dod) != 0 {
		t.Errorf("missing task file should yield empty DoD, got %v", dod)
	}
}

func TestRunEndTask(t *testing.T) {
	dir := chdirTemp(t)
	writeRelFile(t, dir, "docs/tasks/TASKS.md", `| T-101 | init | P0 | — | ⬜ |`)
	writeRelFile(t, dir, "docs/tasks/T-101.md", "# 任务 T-101\n**允许修改文件**：src/a.go\n")

	runBeginTask([]string{"T-101"})
	if tracker.GetTaskID() != "T-101" {
		t.Fatalf("begin-task should set task, got %q", tracker.GetTaskID())
	}

	runWithExitCapture(t, func() { runEndTask(nil) })
	if tracker.GetTaskID() != "" {
		t.Errorf("end-task should clear task, still %q", tracker.GetTaskID())
	}
	ctx := tracker.GetTaskContext()
	if len(ctx.AllowedFiles) != 0 || len(ctx.DoD) != 0 {
		t.Errorf("end-task should clear boundaries, got %+v", ctx)
	}
}

func TestRunBeginTaskNoBoundary(t *testing.T) {
	dir := chdirTemp(t)
	writeRelFile(t, dir, "docs/tasks/TASKS.md", `| T-101 | init | P0 | — | ⬜ |`)
	// Task file exists but has no 允许修改文件 boundary → begin-task must refuse
	// (otherwise check-scope would fail-open).
	writeRelFile(t, dir, "docs/tasks/T-101.md", "# 任务 T-101\n## 验收标准\n- [ ] AC1\n")

	code := runWithExitCapture(t, func() { runBeginTask([]string{"T-101"}) })
	if code != 1 {
		t.Errorf("begin-task without allowed boundary should exit 1, got %d", code)
	}
	if tracker.GetTaskID() != "" {
		t.Errorf("task must not be declared when boundary missing, got %q", tracker.GetTaskID())
	}
}
