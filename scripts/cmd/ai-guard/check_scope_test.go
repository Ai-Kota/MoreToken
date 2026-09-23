package main

import (
	"os"
	"testing"
)

// TestIsTaskDefinitionPath —— 门禁死锁的回归闸。
//
// 判据：TASKS.md 必须被认作"解锁门禁的任务文件"。否则 begin-task 无法校验
// 任务行（它读 TASKS.md），而登记任务行又要先有已声明的任务 → 死循环。
func TestIsTaskDefinitionPath(t *testing.T) {
	tests := []struct {
		path string
		want bool
	}{
		{"docs/tasks/T-001.md", true},
		{"docs/tasks/TASKS.md", true}, // ← 本测试存在的理由
		{"docs/tasks/notes.md", false},
		{"docs/TASKS.md", false}, // 必须在 docs/tasks/ 下
		{"docs/tasks/TASKS.txt", false},
		{"scripts/TASKS.md", false},
	}
	for _, tc := range tests {
		if got := isTaskDefinitionPath(tc.path); got != tc.want {
			t.Errorf("isTaskDefinitionPath(%q) = %v, want %v", tc.path, got, tc.want)
		}
	}
}

// TestIsBlockedPath_TasksBoardWritableWithoutTask —— 无任务时 TASKS.md 必须可写，
// 否则新项目/新任务永远起不来第一步。
func TestIsBlockedPath_TasksBoardWritableWithoutTask(t *testing.T) {
	if isBlockedPath("docs/tasks/TASKS.md") {
		t.Error("无任务时 docs/tasks/TASKS.md 应可写（否则登记新任务需要已声明的任务 = 死锁）")
	}
	if !isBlockedPath("internal/config/config.go") {
		t.Error("业务文件在无任务时仍须被拦（放宽不能溢出到 TASKS.md 之外）")
	}
}

func TestParseFileList(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want []string
	}{
		{"empty", "", nil},
		{"single", "src/a.go", []string{"src/a.go"}},
		{"multi", "src/a.go, docs/b.md", []string{"src/a.go", "docs/b.md"}},
		{"spaces", " a.go , b.go ", []string{"a.go", "b.go"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := parseFileList(tt.in)
			if len(got) != len(tt.want) {
				t.Fatalf("parseFileList(%q) = %v, want %v", tt.in, got, tt.want)
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Errorf("parseFileList(%q)[%d] = %q, want %q", tt.in, i, got[i], tt.want[i])
				}
			}
		})
	}
}

func TestMatchPath(t *testing.T) {
	tests := []struct {
		name  string
		file  string
		pat   string
		want  bool
	}{
		{"exact", "src/a.go", "src/a.go", true},
		{"prefix-dir", "src/a/b.go", "src/", true},
		{"glob", "src/a.go", "src/*.go", true},
		{"glob-suffix", "src/a_test.go", "src/*.go", true},
		{"glob-no", "src/main.py", "src/*.go", false},
		{"mismatch", "docs/a.md", "src/", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := matchPath(tt.file, tt.pat); got != tt.want {
				t.Errorf("matchPath(%q, %q) = %v, want %v", tt.file, tt.pat, got, tt.want)
			}
		})
	}
}

func TestIsDenied(t *testing.T) {
	denied := []string{"src/secret.go", "deploy/"}
	if !isDenied("src/secret.go", denied) {
		t.Error("exact denied file should match")
	}
	if !isDenied("deploy/nats.conf", denied) {
		t.Error("directory-prefix denied should match")
	}
	if isDenied("src/ok.go", denied) {
		t.Error("non-denied file should not match")
	}
	if isDenied("src/secret2.go", denied) {
		t.Error("similar-but-different file should not match")
	}
}

func TestIsAllowed(t *testing.T) {
	allowed := []string{"src/*.go"}
	if !isAllowed("src/main.go", allowed) {
		t.Error("glob-allowed file should match")
	}
	if isAllowed("src/main.py", allowed) {
		t.Error("non-glob-allowed file should not match")
	}
}

func TestIsNewFileAllowed(t *testing.T) {
	dirs := []string{"src", "docs/tasks"}
	if !isNewFileAllowed("src/new.go", dirs) {
		t.Error("new file under allowed dir should be allowed")
	}
	if !isNewFileAllowed("src/nested/new.go", dirs) {
		t.Error("new file under nested allowed dir should be allowed")
	}
	if isNewFileAllowed("tests/new.go", dirs) {
		t.Error("new file outside allowed dirs should be denied")
	}
}

func TestLoadTaskBoundaries(t *testing.T) {
	tmpDir := t.TempDir()
	origWd, _ := os.Getwd()
	os.Chdir(tmpDir)
	defer os.Chdir(origWd)

	writeRelFile(t, tmpDir, "docs/tasks/T-101.md", `# 任务 T-101
**允许修改文件**：src/a.go, docs/b.md
**禁止修改文件**：deploy/
**允许新增文件目录**：src/mod/`)

	// Both bare "101" and full "T-101" forms must resolve to the same file.
	for _, id := range []string{"101", "T-101"} {
		allowed, denied, newDirs := loadTaskBoundaries(id)
		if len(allowed) != 2 || allowed[0] != "src/a.go" {
			t.Errorf("%s: allowed = %v, want [src/a.go docs/b.md]", id, allowed)
		}
		if len(denied) != 1 || denied[0] != "deploy/" {
			t.Errorf("%s: denied = %v, want [deploy/]", id, denied)
		}
		if len(newDirs) != 1 || newDirs[0] != "src/mod/" {
			t.Errorf("%s: newDirs = %v, want [src/mod/]", id, newDirs)
		}
	}
}

func TestLoadTaskBoundariesMissing(t *testing.T) {
	tmpDir := t.TempDir()
	origWd, _ := os.Getwd()
	os.Chdir(tmpDir)
	defer os.Chdir(origWd)

	allowed, denied, newDirs := loadTaskBoundaries("999")
	if allowed != nil || denied != nil || newDirs != nil {
		t.Errorf("missing task file should return nils, got %v/%v/%v", allowed, denied, newDirs)
	}
}
