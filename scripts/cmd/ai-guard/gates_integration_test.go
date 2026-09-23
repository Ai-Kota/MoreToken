package main

import (
	"strings"
	"testing"
)

func TestRunVerifyTaskGoProject(t *testing.T) {
	dir := chdirTemp(t)
	writeRelFile(t, dir, "go.mod", "module demo\n\ngo 1.21\n")
	writeRelFile(t, dir, "src/main.go", "package main\n\nfunc main() {}\n")
	writeRelFile(t, dir, "src/main_test.go", "package main\n\nimport \"testing\"\n\nfunc TestOK(t *testing.T) {}\n")
	writeRelFile(t, dir, "docs/tasks/TASKS.md", `| T-001 | init | P0 | — | 🔄 |`)
	writeRelFile(t, dir, "docs/tasks/SESSION-STATE.md", "# s")

	code := runWithExitCapture(t, func() { runVerifyTask([]string{"T-001"}) })
	if code != -1 && code != 0 {
		t.Errorf("verify-task on passing project should not fail, got %d", code)
	}

	data, err := readFile("docs/tasks/TASKS.md")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(data, "✅") {
		t.Errorf("verify-task should mark task done in TASKS.md:\n%s", data)
	}
}

func TestRunVerifyTaskMissingTask(t *testing.T) {
	dir := chdirTemp(t)
	writeRelFile(t, dir, "go.mod", "module demo\n\ngo 1.21\n")
	writeRelFile(t, dir, "docs/tasks/TASKS.md", `| T-001 | init | P0 | — | 🔄 |`)

	code := runWithExitCapture(t, func() { runVerifyTask([]string{"T-999"}) })
	if code != 1 {
		t.Errorf("verify-task on missing task should exit 1, got %d", code)
	}
}

func TestRunVerifyTaskFailingTests(t *testing.T) {
	dir := chdirTemp(t)
	writeRelFile(t, dir, "go.mod", "module demo\n\ngo 1.21\n")
	writeRelFile(t, dir, "src/main.go", "package main\n\nfunc main() {}\n")
	// Test that always fails.
	writeRelFile(t, dir, "src/main_test.go", "package main\n\nimport \"testing\"\n\nfunc TestFail(t *testing.T) { t.Fatal(\"boom\") }\n")
	writeRelFile(t, dir, "docs/tasks/TASKS.md", `| T-001 | init | P0 | — | 🔄 |`)
	writeRelFile(t, dir, "docs/tasks/SESSION-STATE.md", "# s")

	code := runWithExitCapture(t, func() { runVerifyTask([]string{"T-001"}) })
	if code != 1 {
		t.Errorf("verify-task with failing tests should exit 1, got %d", code)
	}
}

func TestRunWuGuardNonBashAllowed(t *testing.T) {
	chdirTemp(t)
	withStdin(t, `{"tool_name":"Read","tool_input":{}}`, func() {
		code := runWithExitCapture(t, func() { runWuGuard(nil) })
		if code != 0 {
			t.Errorf("non-Bash hook input should allow, got %d", code)
		}
	})
}

func TestRunWuGuardNonCommitBashAllowed(t *testing.T) {
	chdirTemp(t)
	withStdin(t, `{"tool_name":"Bash","tool_input":{"command":"git status"}}`, func() {
		code := runWithExitCapture(t, func() { runWuGuard(nil) })
		if code != 0 {
			t.Errorf("non-commit bash should allow, got %d", code)
		}
	})
}

func TestRunWuGuardCommitUnderLimit(t *testing.T) {
	dir := chdirTemp(t)
	writeRelFile(t, dir, "a.txt", "1")
	initGitRepo(t, dir)
	writeRelFile(t, dir, "b.txt", "2")
	gitCmd(t, "add", ".")

	withStdin(t, `{"tool_name":"Bash","tool_input":{"command":"git commit -m \"feat: add b (WU-2-03)\""}}`, func() {
		code := runWithExitCapture(t, func() { runWuGuard(nil) })
		if code != 0 {
			t.Errorf("small commit should allow, got %d", code)
		}
	})
}

func TestRunWuGuardOverLimitBlocked(t *testing.T) {
	dir := chdirTemp(t)
	writeRelFile(t, dir, "a.txt", "1")
	initGitRepo(t, dir)
	// Stage more than maxFilesPerCommit files with no WU marker.
	for i := 0; i < maxFilesPerCommit+2; i++ {
		writeRelFile(t, dir, "extra"+string(rune('a'+i))+".txt", "x")
	}
	gitCmd(t, "add", ".")

	withStdin(t, `{"tool_name":"Bash","tool_input":{"command":"git commit -m \"feat: bulk\""}}`, func() {
		code := runWithExitCapture(t, func() { runWuGuard(nil) })
		if code != 2 {
			t.Errorf("oversized commit without WU marker should block (exit 2), got %d", code)
		}
	})
}

func TestRunWuGuardOverLimitWithMarker(t *testing.T) {
	dir := chdirTemp(t)
	writeRelFile(t, dir, "a.txt", "1")
	initGitRepo(t, dir)
	for i := 0; i < maxFilesPerCommit+2; i++ {
		writeRelFile(t, dir, "extra"+string(rune('a'+i))+".txt", "x")
	}
	gitCmd(t, "add", ".")

	withStdin(t, `{"tool_name":"Bash","tool_input":{"command":"git commit -m \"refactor: bulk (WU-2-03)\""}}`, func() {
		code := runWithExitCapture(t, func() { runWuGuard(nil) })
		if code != 0 {
			t.Errorf("oversized commit WITH WU marker should allow, got %d", code)
		}
	})
}
