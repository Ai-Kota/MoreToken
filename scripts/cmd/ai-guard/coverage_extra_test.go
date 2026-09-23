package main

import (
	"os"
	"testing"
)

func TestMainVersionBranch(t *testing.T) {
	chdirTemp(t)
	oldArgs := os.Args
	defer func() { os.Args = oldArgs }()
	os.Args = []string{"ai-guard", "version"}
	// version branch prints and returns without os.Exit.
	main()
}

func TestMainHelpBranch(t *testing.T) {
	chdirTemp(t)
	oldArgs := os.Args
	defer func() { os.Args = oldArgs }()
	os.Args = []string{"ai-guard", "--help"}
	main()
}

func TestBuildSelfFailure(t *testing.T) {
	// Build from a dir with no cmd/ai-guard → build fails → exit 1.
	dir := chdirTemp(t)
	writeRelFile(t, dir, "go.mod", "module demo\n\ngo 1.21\n")
	code := runWithExitCapture(t, func() { buildSelf() })
	if code != 1 {
		t.Errorf("buildSelf with missing source should exit 1, got %d", code)
	}
}

func TestRunPreTaskCheckTaskDone(t *testing.T) {
	dir := chdirTemp(t)
	writeRelFile(t, dir, "docs/tasks/TASKS.md", `| T-001 | init | P0 | — | ✅ |`)
	writeRelFile(t, dir, "docs/project/GOALS.md", "# goals")
	writeRelFile(t, dir, "docs/architecture/ARCHITECTURE.md", "# arch")
	writeRelFile(t, dir, "docs/constraints/ANTI-HALLUCINATION.md", "# anti")
	writeRelFile(t, dir, "docs/constraints/AUTHORIZATION-MODEL.md", "# auth")
	writeRelFile(t, dir, "docs/tasks/SESSION-STATE.md", "# s")

	// A completed task is a warning, not a blocker → exit 0.
	code := runWithExitCapture(t, func() { runPreTaskCheck([]string{"T-001"}) })
	if code != 0 {
		t.Errorf("completed task should not block pre-task-check, got %d", code)
	}
}

func TestRunPreTaskCheckDepIncomplete(t *testing.T) {
	dir := chdirTemp(t)
	writeRelFile(t, dir, "docs/tasks/TASKS.md", `| T-001 | init | P0 | — | ⬜ |
| T-002 | build | P0 | T-001 | ⬜ |`)
	writeRelFile(t, dir, "docs/project/GOALS.md", "# goals")
	writeRelFile(t, dir, "docs/architecture/ARCHITECTURE.md", "# arch")
	writeRelFile(t, dir, "docs/constraints/ANTI-HALLUCINATION.md", "# anti")
	writeRelFile(t, dir, "docs/constraints/AUTHORIZATION-MODEL.md", "# auth")
	writeRelFile(t, dir, "docs/tasks/SESSION-STATE.md", "# s")

	// T-002 depends on T-001 which is ⬜ → error → exit 1.
	code := runWithExitCapture(t, func() { runPreTaskCheck([]string{"T-002"}) })
	if code != 1 {
		t.Errorf("incomplete prerequisite should block, got %d", code)
	}
}

func TestRunCheckScopeAllowedFile(t *testing.T) {
	dir := chdirTemp(t)
	writeRelFile(t, dir, ".claude/read_tracker.json", `{"task_id":"T-101","read_files":[]}`)
	writeRelFile(t, dir, "docs/tasks/T-101.md", `# T-101
**允许修改文件**：src/`)
	withStdin(t, `{"tool_name":"Write","tool_input":{"file_path":"src/a.go"}}`, func() {
		code := runWithExitCapture(t, func() { runCheckScope(nil) })
		if code != 0 {
			t.Errorf("file in allowed list should pass, got %d", code)
		}
	})
}

func TestRunCheckScopeNewFileDenied(t *testing.T) {
	dir := chdirTemp(t)
	writeRelFile(t, dir, ".claude/read_tracker.json", `{"task_id":"T-101","read_files":[]}`)
	writeRelFile(t, dir, "docs/tasks/T-101.md", `# T-101
**允许新增文件目录**：src/newmod/`)
	// Creating a file outside the allowed new-file dirs → blocked (exit 1).
	withStdin(t, `{"tool_name":"Write","tool_input":{"file_path":"other/x.go"}}`, func() {
		code := runWithExitCapture(t, func() { runCheckScope(nil) })
		if code != 1 {
			t.Errorf("new file outside allowed dir should be blocked, got %d", code)
		}
	})
}
