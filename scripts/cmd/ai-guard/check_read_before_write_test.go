package main

import (
	"os"
	"testing"
)

func TestIsTaskBoundaryAllowed(t *testing.T) {
	tmpDir := t.TempDir()
	origWd, _ := os.Getwd()
	os.Chdir(tmpDir)
	defer os.Chdir(origWd)

	writeRelFile(t, tmpDir, ".claude/read_tracker.json", `{"task_id":"T-101","read_files":[]}`)
	writeRelFile(t, tmpDir, "docs/tasks/T-101.md", `# 任务 T-101
**允许新增文件目录**：src/newmod/, tests/fixtures/`)

	if !isTaskBoundaryAllowed("src/newmod/a.go") {
		t.Error("file under first allowed dir should be allowed")
	}
	if !isTaskBoundaryAllowed("tests/fixtures/x.json") {
		t.Error("file under second allowed dir should be allowed")
	}
	if isTaskBoundaryAllowed("src/other/b.go") {
		t.Error("file outside allowed dirs should be denied")
	}
}

func TestIsTaskBoundaryAllowedNoTask(t *testing.T) {
	tmpDir := t.TempDir()
	origWd, _ := os.Getwd()
	os.Chdir(tmpDir)
	defer os.Chdir(origWd)

	// No tracker file → no task context → false.
	if isTaskBoundaryAllowed("src/a.go") {
		t.Error("without task context, boundary allowance should be false")
	}
}

func TestIsTaskBoundaryAllowedNoTaskFile(t *testing.T) {
	tmpDir := t.TempDir()
	origWd, _ := os.Getwd()
	os.Chdir(tmpDir)
	defer os.Chdir(origWd)

	writeRelFile(t, tmpDir, ".claude/read_tracker.json", `{"task_id":"T-101","read_files":[]}`)
	// No docs/tasks/T-101.md file.
	if isTaskBoundaryAllowed("src/a.go") {
		t.Error("without task file, boundary allowance should be false")
	}
}

func TestIsTaskBoundaryAllowedNoNewDirs(t *testing.T) {
	tmpDir := t.TempDir()
	origWd, _ := os.Getwd()
	os.Chdir(tmpDir)
	defer os.Chdir(origWd)

	writeRelFile(t, tmpDir, ".claude/read_tracker.json", `{"task_id":"T-101","read_files":[]}`)
	// Task file exists but has no 允许新增文件目录 section.
	writeRelFile(t, tmpDir, "docs/tasks/T-101.md", `# 任务 T-101
**允许修改文件**：src/a.go`)

	if isTaskBoundaryAllowed("src/new.go") {
		t.Error("task without new-file-dirs section should deny")
	}
}
