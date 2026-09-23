package main

import (
	"os"
	"testing"

	"spt/scripts/internal/tracker"
)

// exitSignal marks an osExit call captured by a test.
type exitSignal struct{ code int }

// runWithExitCapture runs fn with osExit replaced by a recorder, returning the
// exit code (or -1 if fn returned without calling osExit).
func runWithExitCapture(t *testing.T, fn func()) (code int) {
	t.Helper()
	old := osExit
	osExit = func(c int) { code = c; panic(exitSignal{code: c}) }
	defer func() { osExit = old }()
	defer func() {
		if r := recover(); r != nil {
			if _, ok := r.(exitSignal); ok {
				return
			}
			panic(r)
		}
	}()
	fn()
	return -1
}

// withStdin runs fn with os.Stdin reading from input.
func withStdin(t *testing.T, input string, fn func()) {
	t.Helper()
	old := os.Stdin
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	_, _ = w.WriteString(input)
	_ = w.Close()
	os.Stdin = r
	defer func() { os.Stdin = old }()
	fn()
}

func chdirTemp(t *testing.T) string {
	t.Helper()
	tmpDir := t.TempDir()
	origWd, _ := os.Getwd()
	os.Chdir(tmpDir)
	t.Cleanup(func() { os.Chdir(origWd) })
	return tmpDir
}

func TestRunSessionBootstrap(t *testing.T) {
	dir := chdirTemp(t)
	writeRelFile(t, dir, "docs/tasks/TASKS.md", `| T-001 | init | P0 | ✅ | |
| T-002 | build | P0 | ⬜ | |`)
	writeRelFile(t, dir, "docs/tasks/SESSION-STATE.md", "# s\n| **会话摘要** | 上次在做 Sprint 1 | |")
	writeRelFile(t, dir, "CLAUDE.md", "# c\n| 先读后写 | x |\n| 禁止 | y |\n| 自主执行 | z |")

	runWithExitCapture(t, func() { runSessionBootstrap(nil) })
	if !fileExists(".claude/context-snapshot.md") {
		t.Error("context snapshot should be written")
	}
}

func TestRunCheckpoint(t *testing.T) {
	dir := chdirTemp(t)
	writeRelFile(t, dir, "docs/tasks/TASKS.md", `| T-001 | init | P0 | ⬜ | |`)
	runWithExitCapture(t, func() {
		runCheckpoint([]string{"T-001", "--summary", "done", "--no-inject"})
	})
	if !fileExists("docs/tasks/SESSION-STATE.md") {
		t.Error("checkpoint should write SESSION-STATE.md")
	}
}

func TestRunGenerateSettings(t *testing.T) {
	chdirTemp(t)
	runWithExitCapture(t, func() { runGenerateSettings([]string{"2"}) })
	if !fileExists(".claude/settings.json") {
		t.Error("generate-settings should write .claude/settings.json")
	}
}

func TestRunGenerateSettingsInvalidLevel(t *testing.T) {
	chdirTemp(t)
	code := runWithExitCapture(t, func() { runGenerateSettings([]string{"9"}) })
	if code != 1 {
		t.Errorf("invalid level should exit 1, got %d", code)
	}
}

func TestRunConstraintMetricsRecordReset(t *testing.T) {
	chdirTemp(t)
	code := runWithExitCapture(t, func() {
		runConstraintMetrics([]string{"record", "check-scope", "src/a.go", "R1", "blocked"})
	})
	if code != -1 {
		t.Errorf("record should not exit, got %d", code)
	}
	if !fileExists(".claude/metrics/interceptions.json") {
		t.Error("interceptions.json should be written")
	}
	runWithExitCapture(t, func() { runConstraintMetrics([]string{"report"}) })
	runWithExitCapture(t, func() { runConstraintMetrics([]string{"reset"}) })
	if fileExists(".claude/metrics/interceptions.json") {
		t.Error("reset should clear interceptions.json")
	}
}

func TestRunPreTaskCheck(t *testing.T) {
	dir := chdirTemp(t)
	writeRelFile(t, dir, "docs/tasks/TASKS.md", `| T-001 | init | P0 | — | ⬜ |`)
	writeRelFile(t, dir, "docs/project/GOALS.md", "# goals")
	writeRelFile(t, dir, "docs/architecture/ARCHITECTURE.md", "# arch")
	writeRelFile(t, dir, "docs/constraints/ANTI-HALLUCINATION.md", "# anti")
	writeRelFile(t, dir, "docs/constraints/AUTHORIZATION-MODEL.md", "# auth")
	writeRelFile(t, dir, "docs/tasks/SESSION-STATE.md", "# s")

	code := runWithExitCapture(t, func() { runPreTaskCheck([]string{"T-001"}) })
	if code != 0 {
		t.Errorf("pre-task-check on valid task should exit 0, got %d", code)
	}
}

func TestRunPreTaskCheckMissingTask(t *testing.T) {
	dir := chdirTemp(t)
	writeRelFile(t, dir, "docs/tasks/TASKS.md", `| T-001 | init | P0 | — | ⬜ |`)
	code := runWithExitCapture(t, func() { runPreTaskCheck([]string{"T-999"}) })
	if code != 1 {
		t.Errorf("pre-task-check on missing task should exit 1, got %d", code)
	}
}

func TestRunCheckReadBeforeWriteUnreadBlocked(t *testing.T) {
	dir := chdirTemp(t)
	writeRelFile(t, dir, "src/existing.go", "package main")
	withStdin(t, `{"tool_name":"Write","tool_input":{"file_path":"src/existing.go"}}`, func() {
		code := runWithExitCapture(t, func() { runCheckReadBeforeWrite(nil) })
		if code != 1 {
			t.Errorf("unread existing file should be blocked (exit 1), got %d", code)
		}
	})
}

func TestRunCheckReadBeforeWriteReadAllowed(t *testing.T) {
	chdirTemp(t)
	tracker.AddFile("src/read.go")
	withStdin(t, `{"tool_name":"Write","tool_input":{"file_path":"src/read.go"}}`, func() {
		code := runWithExitCapture(t, func() { runCheckReadBeforeWrite(nil) })
		if code != 0 {
			t.Errorf("read file should be allowed (exit 0), got %d", code)
		}
	})
}

func TestRunCheckScopeNoTaskBlocked(t *testing.T) {
	chdirTemp(t)
	// No declared task → project-file writes are blocked (exit 2).
	withStdin(t, `{"tool_name":"Write","tool_input":{"file_path":"src/a.go"}}`, func() {
		code := runWithExitCapture(t, func() { runCheckScope(nil) })
		if code != 2 {
			t.Errorf("no-task context should block project writes (exit 2), got %d", code)
		}
	})
}

func TestRunCheckScopeNoTaskClaudeExempt(t *testing.T) {
	chdirTemp(t)
	// .claude/ internal files are exempt even without a task.
	withStdin(t, `{"tool_name":"Write","tool_input":{"file_path":".claude/metrics/x.json"}}`, func() {
		code := runWithExitCapture(t, func() { runCheckScope(nil) })
		if code != 0 {
			t.Errorf(".claude/ writes should be exempt without task (exit 0), got %d", code)
		}
	})
}

func TestRunCheckDesignDocNonSrcAllowed(t *testing.T) {
	chdirTemp(t)
	withStdin(t, `{"tool_name":"Write","tool_input":{"file_path":"docs/a.md"}}`, func() {
		code := runWithExitCapture(t, func() { runCheckDesignDoc(nil) })
		if code != 0 {
			t.Errorf("non-src file should be allowed (exit 0), got %d", code)
		}
	})
}

func TestRunTrackAndClearRead(t *testing.T) {
	chdirTemp(t)
	withStdin(t, `{"tool_name":"Read","tool_input":{"file_path":"src/x.go"}}`, func() {
		runWithExitCapture(t, func() { runTrackRead(nil) })
	})
	if !tracker.IsRead("src/x.go") {
		t.Error("track-read should record the file")
	}
	withStdin(t, `{"tool_name":"Write","tool_input":{"file_path":"src/x.go"}}`, func() {
		runWithExitCapture(t, func() { runClearRead(nil) })
	})
	trk := tracker.Load()
	for _, f := range trk.ReadFiles {
		if f == "src/x.go" {
			t.Error("clear-read should remove the file entry")
		}
	}
}

func TestRunPostCommitVerifyNonBashAllowed(t *testing.T) {
	chdirTemp(t)
	withStdin(t, `{"tool_name":"Read","tool_input":{}}`, func() {
		code := runWithExitCapture(t, func() { runPostCommitVerify(nil) })
		if code != 0 {
			t.Errorf("non-Bash hook input should allow (exit 0), got %d", code)
		}
	})
}

func TestRunEnvProtectNonBashAllowed(t *testing.T) {
	chdirTemp(t)
	withStdin(t, `{"tool_name":"Read","tool_input":{"file_path":"docs/a.md"}}`, func() {
		code := runWithExitCapture(t, func() { runEnvProtect(nil) })
		if code != 0 {
			t.Errorf("non-Bash hook input should allow (exit 0), got %d", code)
		}
	})
}

func TestRunProjectAssess(t *testing.T) {
	chdirTemp(t)
	code := runWithExitCapture(t, func() { runProjectAssess([]string{"hello world demo"}) })
	if code == 1 {
		t.Errorf("project-assess should not fail, got exit %d", code)
	}
}

func TestRunCheckDesignDocSrcMissingDoc(t *testing.T) {
	chdirTemp(t)
	// src/ file without a design doc → blocked (exit 1).
	withStdin(t, `{"tool_name":"Write","tool_input":{"file_path":"src/orders/service.go"}}`, func() {
		code := runWithExitCapture(t, func() { runCheckDesignDoc(nil) })
		if code != 1 {
			t.Errorf("src file without design doc should be blocked, got %d", code)
		}
	})
}

func TestRunCheckDesignDocSrcWithDoc(t *testing.T) {
	dir := chdirTemp(t)
	writeRelFile(t, dir, "docs/architecture/modules/orders.md", "# orders module")
	withStdin(t, `{"tool_name":"Write","tool_input":{"file_path":"src/orders/service.go"}}`, func() {
		code := runWithExitCapture(t, func() { runCheckDesignDoc(nil) })
		if code != 0 {
			t.Errorf("src file with design doc should be allowed, got %d", code)
		}
	})
}

func TestRunCheckScopeDeniedFile(t *testing.T) {
	dir := chdirTemp(t)
	writeRelFile(t, dir, ".claude/read_tracker.json", `{"task_id":"T-101","read_files":[]}`)
	writeRelFile(t, dir, "docs/tasks/T-101.md", `# T-101
**禁止修改文件**：deploy/`)
	withStdin(t, `{"tool_name":"Write","tool_input":{"file_path":"deploy/nats.conf"}}`, func() {
		code := runWithExitCapture(t, func() { runCheckScope(nil) })
		if code != 1 {
			t.Errorf("denied file should be blocked, got %d", code)
		}
	})
}

func TestRunCheckReadBeforeWriteNewSrcFile(t *testing.T) {
	chdirTemp(t)
	// New file under src/ is allowed even without read.
	withStdin(t, `{"tool_name":"Write","tool_input":{"file_path":"src/newfile.go"}}`, func() {
		code := runWithExitCapture(t, func() { runCheckReadBeforeWrite(nil) })
		if code != 0 {
			t.Errorf("new src file should be allowed, got %d", code)
		}
	})
}

func TestRunConstraintMetricsNoArgs(t *testing.T) {
	chdirTemp(t)
	code := runWithExitCapture(t, func() { runConstraintMetrics(nil) })
	if code != 1 {
		t.Errorf("constraint-metrics without subcommand should exit 1, got %d", code)
	}
}

func TestRunConstraintMetricsRecordTooFewArgs(t *testing.T) {
	chdirTemp(t)
	code := runWithExitCapture(t, func() { runConstraintMetrics([]string{"record", "only"}) })
	if code != 1 {
		t.Errorf("record with too few args should exit 1, got %d", code)
	}
}

func TestRunCheckScopeInitWindowDocsAllowed(t *testing.T) {
	chdirTemp(t)
	// Fresh project with no TASKS.md → init window allows docs/** scaffolding.
	withStdin(t, `{"tool_name":"Write","tool_input":{"file_path":"docs/project/GOALS.md"}}`, func() {
		code := runWithExitCapture(t, func() { runCheckScope(nil) })
		if code != 0 {
			t.Errorf("init window should allow docs/ writes (exit 0), got %d", code)
		}
	})
}

func TestRunCheckScopeInitWindowRootMdAllowed(t *testing.T) {
	chdirTemp(t)
	// Fresh project → root markdown scaffolding allowed.
	withStdin(t, `{"tool_name":"Write","tool_input":{"file_path":"README.md"}}`, func() {
		code := runWithExitCapture(t, func() { runCheckScope(nil) })
		if code != 0 {
			t.Errorf("init window should allow root .md (exit 0), got %d", code)
		}
	})
}

func TestRunCheckScopeInitWindowSrcBlocked(t *testing.T) {
	chdirTemp(t)
	// Fresh project: business code still blocked even in init window.
	withStdin(t, `{"tool_name":"Write","tool_input":{"file_path":"src/a.go"}}`, func() {
		code := runWithExitCapture(t, func() { runCheckScope(nil) })
		if code != 2 {
			t.Errorf("init window must still block business code (exit 2), got %d", code)
		}
	})
}

func TestRunCheckScopeStaleTaskCleared(t *testing.T) {
	dir := chdirTemp(t)
	// Tracker has a stale task_id whose definition file is gone.
	writeRelFile(t, dir, ".claude/read_tracker.json", `{"task_id":"T-101","read_files":[]}`)
	writeRelFile(t, dir, "docs/tasks/TASKS.md", `| T-101 | init | P0 | — | ⬜ |`)

	withStdin(t, `{"tool_name":"Write","tool_input":{"file_path":"src/a.go"}}`, func() {
		code := runWithExitCapture(t, func() { runCheckScope(nil) })
		if code != 2 {
			t.Errorf("stale task (missing T-101.md) should be blocked, got %d", code)
		}
	})
	// The stale context must be cleared so it can't silently allow writes later.
	if tracker.GetTaskID() != "" {
		t.Errorf("stale task_id should be cleared, still %q", tracker.GetTaskID())
	}
}

func TestRunCheckScopeTASKSFileInitsWindowClosed(t *testing.T) {
	dir := chdirTemp(t)
	// Real-world TASKS.md starts with a heading — the (?m) regex must still see
	// the task row and close the init window.
	writeRelFile(t, dir, "docs/tasks/TASKS.md", "# 任务清单\n\n| T-101 | init | P0 | — | ⬜ |\n")
	withStdin(t, `{"tool_name":"Write","tool_input":{"file_path":"docs/project/GOALS.md"}}`, func() {
		code := runWithExitCapture(t, func() { runCheckScope(nil) })
		if code != 2 {
			t.Errorf("with task definitions present, un-scoped docs write must block (exit 2), got %d", code)
		}
	})
}

func TestRunCheckScopeTaskDefinitionExempt(t *testing.T) {
	dir := chdirTemp(t)
	// Once a task row exists, the window closes — but creating the per-task
	// definition file itself must stay allowed (planning layer).
	writeRelFile(t, dir, "docs/tasks/TASKS.md", "# 任务清单\n\n| T-101 | init | P0 | — | 🔄 |\n")
	withStdin(t, `{"tool_name":"Write","tool_input":{"file_path":"docs/tasks/T-101.md"}}`, func() {
		code := runWithExitCapture(t, func() { runCheckScope(nil) })
		if code != 0 {
			t.Errorf("task definition file should always be writable (exit 0), got %d", code)
		}
	})
}

func TestReadHookInput(t *testing.T) {
	withStdin(t, `{"tool_name":"Bash","tool_input":{"command":"git commit -m x"}}`, func() {
		in, err := readHookInput()
		if err != nil {
			t.Fatal(err)
		}
		if in.ToolName != "Bash" || in.Input["command"] != "git commit -m x" {
			t.Errorf("parsed input mismatch: %+v", in)
		}
	})
}
