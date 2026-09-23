package main

import (
	"os/exec"
	"testing"
)

// gitCmd runs a git command in the current (temp) working directory.
func gitCmd(t *testing.T, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v failed: %v\n%s", args, err, out)
	}
}

func initGitRepo(t *testing.T, dir string) {
	t.Helper()
	gitCmd(t, "init")
	gitCmd(t, "config", "user.email", "test@example.com")
	gitCmd(t, "config", "user.name", "test")
	gitCmd(t, "add", ".")
	gitCmd(t, "commit", "-m", "init")
}

func TestRunPostCommitVerifyGitCommit(t *testing.T) {
	dir := chdirTemp(t)
	writeRelFile(t, dir, "go.mod", "module demo\n\ngo 1.21\n")
	writeRelFile(t, dir, "src/main.go", "package main\n\nfunc main() {}\n")
	writeRelFile(t, dir, "src/main_test.go", "package main\n\nimport \"testing\"\n\nfunc TestOK(t *testing.T) {}\n")
	initGitRepo(t, dir)

	// Modify + stage a change, then fire a git commit with a task marker.
	writeRelFile(t, dir, "src/main.go", "package main\n\nfunc main() { _ = 1 }\n")
	gitCmd(t, "add", ".")

	withStdin(t, `{"tool_name":"Bash","tool_input":{"command":"git commit -m \"feat: x (T-001)\""}}`, func() {
		code := runWithExitCapture(t, func() { runPostCommitVerify(nil) })
		if code != 0 {
			t.Errorf("verify-commit with passing tests should allow, got exit %d", code)
		}
	})
}

func TestRunPostCommitVerifyNoTaskMarker(t *testing.T) {
	dir := chdirTemp(t)
	writeRelFile(t, dir, "a.txt", "1")
	initGitRepo(t, dir)
	// Commit without a T-XXX/WU marker → allow + hint (exit 0).
	withStdin(t, `{"tool_name":"Bash","tool_input":{"command":"git commit -m \"chore: bump\""}}`, func() {
		code := runWithExitCapture(t, func() { runPostCommitVerify(nil) })
		if code != 0 {
			t.Errorf("no-task-marker commit should allow, got exit %d", code)
		}
	})
}

func TestGetCheckpointChangedFiles(t *testing.T) {
	dir := chdirTemp(t)
	writeRelFile(t, dir, "a.txt", "1")
	writeRelFile(t, dir, "b.txt", "2")
	initGitRepo(t, dir)

	writeRelFile(t, dir, "a.txt", "changed")
	changed := getCheckpointChangedFiles()
	if len(changed) == 0 {
		t.Fatal("expected changed files after modifying a.txt")
	}
	found := false
	for _, f := range changed {
		if f == "a.txt" {
			found = true
		}
	}
	if !found {
		t.Errorf("expected a.txt in changed files, got %v", changed)
	}
}

func TestGetCheckpointChangedFilesNoChanges(t *testing.T) {
	dir := chdirTemp(t)
	writeRelFile(t, dir, "a.txt", "1")
	initGitRepo(t, dir)
	if changed := getCheckpointChangedFiles(); len(changed) != 0 {
		t.Errorf("clean tree should have no changed files, got %v", changed)
	}
}
