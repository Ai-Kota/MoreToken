package main

import (
	"os"
	"testing"
)

func TestIsGitCommit(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want bool
	}{
		{"git commit", "git commit -m \"msg\"", true},
		{"git commit amended", "git commit --amend", true},
		{"git -c config commit", "git -c user.name=x commit -m msg", true},
		{"git add", "git add .", false},
		{"git status", "git status", false},
		{"git log", "git log", false},
		{"non-git", "npm test", false},
		{"empty", "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isGitCommit(tt.in); got != tt.want {
				t.Errorf("isGitCommit(%q) = %v, want %v", tt.in, got, tt.want)
			}
		})
	}
}

func TestHasWUFlag(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want bool
	}{
		{"wu marker", "feat(ui): tab (WU-2-03)", true},
		{"wu lowercase", "fix: x (wu-2-03)", true},
		{"bypass token", "big refactor BYPASS-WU-SCOPE", true},
		{"no marker", "feat(ui): add tab", false},
		{"task only", "fix: x (T-108)", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := hasWUFlag(tt.in); got != tt.want {
				t.Errorf("hasWUFlag(%q) = %v, want %v", tt.in, got, tt.want)
			}
		})
	}
}

func TestFindGitDir(t *testing.T) {
	// With a .git dir in cwd → finds it.
	tmpDir := t.TempDir()
	origWd, _ := os.Getwd()
	os.Chdir(tmpDir)
	defer os.Chdir(origWd)

	if err := os.Mkdir(".git", 0755); err != nil {
		t.Fatal(err)
	}
	got := findGitDir()
	if got != tmpDir {
		t.Errorf("findGitDir = %q, want %q", got, tmpDir)
	}
}

func TestFindGitDirNested(t *testing.T) {
	// .git in a parent → walks up and finds it.
	parent := t.TempDir()
	nested := parent + string(os.PathSeparator) + "a" + string(os.PathSeparator) + "b"
	if err := os.MkdirAll(nested, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(parent+"/.git", 0755); err != nil {
		t.Fatal(err)
	}

	origWd, _ := os.Getwd()
	os.Chdir(nested)
	defer os.Chdir(origWd)

	got := findGitDir()
	if got != parent {
		t.Errorf("findGitDir = %q, want %q", got, parent)
	}
}

func TestFindGitDirNone(t *testing.T) {
	tmpDir := t.TempDir()
	origWd, _ := os.Getwd()
	os.Chdir(tmpDir)
	defer os.Chdir(origWd)

	if got := findGitDir(); got != "" {
		t.Errorf("findGitDir without .git = %q, want empty", got)
	}
}

func TestCountModules(t *testing.T) {
	files := []string{
		"src/a.go", "src/b.go", "src/nested/c.go",
		"docs/c.md", "deploy/nats.conf", "go.mod",
	}
	mods := countModules(files)
	if mods["src"] != 2 {
		t.Errorf("src module count = %d, want 2 (got %v)", mods["src"], mods)
	}
	if mods["src/nested"] != 1 {
		t.Errorf("src/nested count = %d, want 1", mods["src/nested"])
	}
	if mods["docs"] != 1 {
		t.Errorf("docs count = %d, want 1", mods["docs"])
	}
	if mods["deploy"] != 1 {
		t.Errorf("deploy count = %d, want 1", mods["deploy"])
	}
	if mods["root"] != 1 {
		t.Errorf("root count = %d, want 1 (go.mod)", mods["root"])
	}
}

func TestCountModulesEmpty(t *testing.T) {
	if mods := countModules(nil); len(mods) != 0 {
		t.Errorf("empty files should yield empty map, got %v", mods)
	}
}
