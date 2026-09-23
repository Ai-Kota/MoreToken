package tracker

import (
	"os"
	"testing"
)

func TestAddFileIsRead(t *testing.T) {
	tmpDir := t.TempDir()
	origWd, _ := os.Getwd()
	os.Chdir(tmpDir)
	defer os.Chdir(origWd)

	AddFile("src/a.go")
	if !IsRead("src/a.go") {
		t.Error("file should be tracked as read after AddFile")
	}
}

func TestAddFileDedup(t *testing.T) {
	tmpDir := t.TempDir()
	origWd, _ := os.Getwd()
	os.Chdir(tmpDir)
	defer os.Chdir(origWd)

	AddFile("src/a.go")
	AddFile("src/a.go")
	tracker := Load()
	count := 0
	for _, f := range tracker.ReadFiles {
		if f == "src/a.go" {
			count++
		}
	}
	if count != 1 {
		t.Errorf("AddFile should dedupe, got %d entries", count)
	}
}

func TestRemoveFile(t *testing.T) {
	tmpDir := t.TempDir()
	origWd, _ := os.Getwd()
	os.Chdir(tmpDir)
	defer os.Chdir(origWd)

	AddFile("src/a.go")
	RemoveFile("src/a.go")

	// The exact file entry is gone (parent dir "src" may remain — that's
	// intentional, it still permits new-file creation under src/).
	trk := Load()
	for _, f := range trk.ReadFiles {
		if f == "src/a.go" {
			t.Errorf("src/a.go should be removed from tracker, got %v", trk.ReadFiles)
		}
	}
}

func TestIsReadParentDir(t *testing.T) {
	tmpDir := t.TempDir()
	origWd, _ := os.Getwd()
	os.Chdir(tmpDir)
	defer os.Chdir(origWd)

	AddFile("src")
	if !IsRead("src/main.go") {
		t.Error("reading a dir should allow files under it")
	}
}

func TestSetGetTaskID(t *testing.T) {
	tmpDir := t.TempDir()
	origWd, _ := os.Getwd()
	os.Chdir(tmpDir)
	defer os.Chdir(origWd)

	SetTaskID("T-001")
	if GetTaskID() != "T-001" {
		t.Errorf("GetTaskID = %q, want T-001", GetTaskID())
	}
}

func TestGetTaskIDEmpty(t *testing.T) {
	tmpDir := t.TempDir()
	origWd, _ := os.Getwd()
	os.Chdir(tmpDir)
	defer os.Chdir(origWd)

	if GetTaskID() != "" {
		t.Errorf("fresh tracker GetTaskID = %q, want empty", GetTaskID())
	}
}

func TestUniqueAppend(t *testing.T) {
	s := uniqueAppend([]string{"a", "b"}, "a")
	if len(s) != 2 {
		t.Errorf("uniqueAppend existing = %v", s)
	}
	s = uniqueAppend([]string{"a"}, "b")
	if len(s) != 2 || s[1] != "b" {
		t.Errorf("uniqueAppend new = %v", s)
	}
}

func TestRemove(t *testing.T) {
	s := remove([]string{"a", "b", "a"}, "a")
	if len(s) != 1 || s[0] != "b" {
		t.Errorf("remove = %v, want [b]", s)
	}
	s = remove([]string{"x", "y"}, "z")
	if len(s) != 2 {
		t.Errorf("remove absent should not change slice, got %v", s)
	}
}

func TestBeginTaskContext(t *testing.T) {
	tmpDir := t.TempDir()
	origWd, _ := os.Getwd()
	os.Chdir(tmpDir)
	defer os.Chdir(origWd)

	BeginTask("T-101", []string{"src/a.go"}, []string{"deploy/"}, []string{"src/newmod/"}, []string{"AC1: login"})
	ctx := GetTaskContext()
	if ctx.TaskID != "T-101" {
		t.Errorf("TaskID = %q, want T-101", ctx.TaskID)
	}
	if len(ctx.AllowedFiles) != 1 || ctx.AllowedFiles[0] != "src/a.go" {
		t.Errorf("AllowedFiles = %v", ctx.AllowedFiles)
	}
	if len(ctx.DeniedFiles) != 1 || ctx.DeniedFiles[0] != "deploy/" {
		t.Errorf("DeniedFiles = %v", ctx.DeniedFiles)
	}
	if len(ctx.NewFileDirs) != 1 || ctx.NewFileDirs[0] != "src/newmod/" {
		t.Errorf("NewFileDirs = %v", ctx.NewFileDirs)
	}
	if len(ctx.DoD) != 1 || ctx.DoD[0] != "AC1: login" {
		t.Errorf("DoD = %v", ctx.DoD)
	}
	if ctx.TaskStartedAt == "" {
		t.Error("TaskStartedAt should be set")
	}
}

func TestGetTaskContextEmpty(t *testing.T) {
	tmpDir := t.TempDir()
	origWd, _ := os.Getwd()
	os.Chdir(tmpDir)
	defer os.Chdir(origWd)

	ctx := GetTaskContext()
	if ctx.TaskID != "" {
		t.Errorf("fresh tracker TaskID = %q, want empty", ctx.TaskID)
	}
	if len(ctx.AllowedFiles) != 0 || len(ctx.DeniedFiles) != 0 || len(ctx.NewFileDirs) != 0 || len(ctx.DoD) != 0 {
		t.Errorf("fresh tracker context should be all-empty, got %+v", ctx)
	}
}

func TestCorruptTrackerFile(t *testing.T) {
	tmpDir := t.TempDir()
	origWd, _ := os.Getwd()
	os.Chdir(tmpDir)
	defer os.Chdir(origWd)

	os.MkdirAll(".claude", 0755)
	os.WriteFile(TrackerFile, []byte("{not valid json"), 0644)
	if GetTaskID() != "" {
		t.Error("corrupt tracker should degrade to empty state")
	}
	// And AddFile should still work afterwards (recovers).
	AddFile("src/x.go")
	if !IsRead("src/x.go") {
		t.Error("should recover after corrupt file")
	}
}

func TestClearTask(t *testing.T) {
	tmpDir := t.TempDir()
	origWd, _ := os.Getwd()
	os.Chdir(tmpDir)
	defer os.Chdir(origWd)

	BeginTask("T-101", []string{"src/a.go"}, []string{"deploy/"}, nil, []string{"AC1: login"})
	ClearTask()
	ctx := GetTaskContext()
	if ctx.TaskID != "" || len(ctx.AllowedFiles) != 0 || len(ctx.DeniedFiles) != 0 || len(ctx.DoD) != 0 {
		t.Errorf("ClearTask should wipe the whole context, got %+v", ctx)
	}
}
