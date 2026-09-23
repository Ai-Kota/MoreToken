package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCheckTestPresence(t *testing.T) {
	tmpDir := t.TempDir()
	origWd, _ := os.Getwd()
	os.Chdir(tmpDir)
	defer os.Chdir(origWd)

	// Test with no tests
	d := checkTestPresence()
	if d.Score != 0 {
		t.Errorf("expected 0 score with no tests, got %d", d.Score)
	}

	// Test with Go tests
	srcDir := filepath.Join(tmpDir, "src")
	os.MkdirAll(srcDir, 0755)
	for i := 0; i < 5; i++ {
		os.WriteFile(filepath.Join(srcDir, "file"+string(rune('a'+i))+"_test.go"), []byte("package main"), 0644)
	}

	d = checkTestPresence()
	if d.Score != 15 {
		t.Errorf("expected 15 score with 5 test files, got %d", d.Score)
	}

	// Add more tests to reach 10+
	for i := 5; i < 10; i++ {
		os.WriteFile(filepath.Join(srcDir, "file"+string(rune('a'+i))+"_test.go"), []byte("package main"), 0644)
	}

	d = checkTestPresence()
	if d.Score != 25 {
		t.Errorf("expected 25 score with 10+ test files, got %d", d.Score)
	}
}

func TestCheckTestCoverageLevel(t *testing.T) {
	tmpDir := t.TempDir()
	origWd, _ := os.Getwd()
	os.Chdir(tmpDir)
	defer os.Chdir(origWd)

	// No project files
	d := checkTestCoverageLevel()
	if d.Score != 0 {
		t.Errorf("expected 0 score for unknown project type, got %d", d.Score)
	}

	// Go project
	os.WriteFile("go.mod", []byte("module test"), 0644)
	d = checkTestCoverageLevel()
	if d.Score != 5 {
		t.Errorf("expected 5 score for Go project, got %d", d.Score)
	}
}

func TestCheckDocsCompleteness(t *testing.T) {
	tmpDir := t.TempDir()
	origWd, _ := os.Getwd()
	os.Chdir(tmpDir)
	defer os.Chdir(origWd)

	// No docs
	d := checkDocsCompleteness()
	if d.Score != 0 {
		t.Errorf("expected 0 score with no docs, got %d", d.Score)
	}

	// Add placeholder README
	os.WriteFile("README.md", []byte("# [项目名]"), 0644)
	d = checkDocsCompleteness()
	if d.Score != 0 {
		t.Errorf("expected 0 score for placeholder README, got %d", d.Score)
	}

	// Add real README (>200 chars)
	readmeContent := "# Real Project\n\nThis is a real project with substantial content that exceeds 200 characters. " + strings.Repeat("Content line with enough characters to pass threshold. ", 8)
	os.WriteFile("README.md", []byte(readmeContent), 0644)
	d = checkDocsCompleteness()
	if d.Score < 5 {
		t.Errorf("expected at least 5 for real README, got %d", d.Score)
	}

	// Add GOALS.md
	os.MkdirAll("docs/project", 0755)
	os.WriteFile("docs/project/GOALS.md", []byte("# Goals\n\nReal content here"), 0644)
	d = checkDocsCompleteness()
	if d.Score < 10 {
		t.Errorf("expected at least 10 with README + GOALS, got %d", d.Score)
	}

	// Add ADRs
	os.MkdirAll("docs/project/ADR", 0755)
	os.WriteFile("docs/project/ADR/ADR-001-scope.md", []byte("# ADR-001\n\n状态: 已接受"), 0644)
	os.WriteFile("docs/project/ADR/ADR-002-tech.md", []byte("# ADR-002\n\n状态: 已接受"), 0644)
	d = checkDocsCompleteness()
	if d.Score != 20 {
		t.Errorf("expected 20 score with all docs, got %d", d.Score)
	}
}

func TestCheckArchitectureDocs(t *testing.T) {
	tmpDir := t.TempDir()
	origWd, _ := os.Getwd()
	os.Chdir(tmpDir)
	defer os.Chdir(origWd)

	// No docs
	d := checkArchitectureDocs()
	if d.Score != 0 {
		t.Errorf("expected 0 score with no arch docs, got %d", d.Score)
	}

	// Placeholder ARCHITECTURE.md
	os.MkdirAll("docs/architecture", 0755)
	os.WriteFile("docs/architecture/ARCHITECTURE.md", []byte("# [项目名]"), 0644)
	d = checkArchitectureDocs()
	if d.Score != 0 {
		t.Errorf("expected 0 for placeholder ARCHITECTURE.md, got %d", d.Score)
	}

	// Real ARCHITECTURE.md (>500 chars)
	archContent := "# Architecture\n\n" + strings.Repeat("Real content with enough characters to pass the 500 character threshold for architecture documentation. ", 8)
	os.WriteFile("docs/architecture/ARCHITECTURE.md", []byte(archContent), 0644)
	d = checkArchitectureDocs()
	if d.Score < 10 {
		t.Errorf("expected at least 10 for real ARCHITECTURE.md, got %d", d.Score)
	}

	// Add module docs
	os.MkdirAll("docs/architecture/modules", 0755)
	os.WriteFile("docs/architecture/modules/module1.md", []byte("# Module 1"), 0644)
	d = checkArchitectureDocs()
	if d.Score != 15 {
		t.Errorf("expected 15 with ARCHITECTURE + modules, got %d", d.Score)
	}
}

func TestCheckCodeStructure(t *testing.T) {
	tmpDir := t.TempDir()
	origWd, _ := os.Getwd()
	os.Chdir(tmpDir)
	defer os.Chdir(origWd)

	// No structure
	d := checkCodeStructure()
	if d.Score != 0 {
		t.Errorf("expected 0 score with no structure, got %d", d.Score)
	}

	// Add src dir
	os.MkdirAll("src", 0755)
	d = checkCodeStructure()
	if d.Score < 5 {
		t.Errorf("expected at least 5 for src dir, got %d", d.Score)
	}

	// Add go.mod
	os.WriteFile("go.mod", []byte("module test"), 0644)
	d = checkCodeStructure()
	if d.Score < 8 {
		t.Errorf("expected at least 8 with src + go.mod, got %d", d.Score)
	}

	// Add lint config
	os.WriteFile(".golangci.yml", []byte("linters: {}"), 0644)
	d = checkCodeStructure()
	if d.Score < 11 {
		t.Errorf("expected at least 11 with lint config, got %d", d.Score)
	}

	// Add CI
	os.MkdirAll(".github/workflows", 0755)
	os.WriteFile(".github/workflows/ci.yml", []byte("name: CI"), 0644)
	d = checkCodeStructure()
	if d.Score < 13 {
		t.Errorf("expected at least 13 with CI, got %d", d.Score)
	}

	// Add .gitignore
	os.WriteFile(".gitignore", []byte("*.exe"), 0644)
	d = checkCodeStructure()
	if d.Score != 15 {
		t.Errorf("expected 15 with all structure, got %d", d.Score)
	}
}

func TestCheckTaskManagement(t *testing.T) {
	tmpDir := t.TempDir()
	origWd, _ := os.Getwd()
	os.Chdir(tmpDir)
	defer os.Chdir(origWd)

	// No task files
	d := checkTaskManagement()
	if d.Score != 0 {
		t.Errorf("expected 0 score with no tasks, got %d", d.Score)
	}

	// TASKS.md with tasks
	os.MkdirAll("docs/tasks", 0755)
	os.WriteFile("docs/tasks/TASKS.md", []byte("| T-001 | task | ⬜ |"), 0644)
	d = checkTaskManagement()
	if d.Score < 5 {
		t.Errorf("expected at least 5 for TASKS.md, got %d", d.Score)
	}

	// Add completed task
	os.WriteFile("docs/tasks/TASKS.md", []byte("| T-001 | task | ✅ |\n| T-002 | task | 🔄 |"), 0644)
	d = checkTaskManagement()
	if d.Score < 8 {
		t.Errorf("expected at least 8 with completed tasks, got %d", d.Score)
	}

	// Add SESSION-STATE.md (>200 chars, no placeholder)
	sessionContent := "# Session State\n\nReal content here with substantial text that exceeds 200 characters. " + strings.Repeat("Content line with enough characters to pass threshold. ", 8)
	os.WriteFile("docs/tasks/SESSION-STATE.md", []byte(sessionContent), 0644)
	d = checkTaskManagement()
	if d.Score != 10 {
		t.Errorf("expected 10 with all task management, got %d", d.Score)
	}
}

func TestAssessProjectMaturityLevels(t *testing.T) {
	tmpDir := t.TempDir()
	origWd, _ := os.Getwd()
	os.Chdir(tmpDir)
	defer os.Chdir(origWd)

	// Level 1 (default) - minimal setup
	report := assessProjectMaturity()
	if report.Level != 1 {
		t.Errorf("expected Level 1 for empty project, got Level %d", report.Level)
	}
	if report.LevelName != "宽松" {
		t.Errorf("expected '宽松' for Level 1, got %s", report.LevelName)
	}

	// Level 2 - add enough for 40 points
	os.MkdirAll("src", 0755)
// Add 10 test files for max test score
for i := 0; i < 10; i++ {
	os.WriteFile(filepath.Join("src", fmt.Sprintf("file%d_test.go", i)), []byte("package main"), 0644)
}
os.WriteFile("go.mod", []byte("module test"), 0644)
// README > 200 chars
readmeContent := "# Real Project\n\n" + strings.Repeat("Content line with enough characters to pass threshold. ", 8)
os.WriteFile("README.md", []byte(readmeContent), 0644)
os.MkdirAll("docs/project", 0755)
os.WriteFile("docs/project/GOALS.md", []byte("# Goals\n\nReal content"), 0644)
os.WriteFile("docs/project/ADR/ADR-001-scope.md", []byte("# ADR-001\n\n状态: 已接受"), 0644)
os.WriteFile("docs/project/ADR/ADR-002-tech.md", []byte("# ADR-002\n\n状态: 已接受"), 0644)
os.MkdirAll("docs/architecture", 0755)
// ARCHITECTURE > 500 chars
archContent := "# Architecture\n\n" + strings.Repeat("Real content with enough characters to pass the 500 character threshold for architecture documentation. ", 8)
os.WriteFile("docs/architecture/ARCHITECTURE.md", []byte(archContent), 0644)
os.MkdirAll("docs/architecture/modules", 0755)
os.WriteFile("docs/architecture/modules/module1.md", []byte("# Module 1"), 0644)
os.MkdirAll("docs/tasks", 0755)
os.WriteFile("docs/tasks/TASKS.md", []byte("| T-001 | task | ✅ |\n| T-002 | task | 🔄 |\n| T-003 | task | ⬜ |"), 0644)
// SESSION-STATE > 200 chars, no placeholder
sessionContent := "# Session State\n\nReal content here with substantial text that exceeds 200 characters. " + strings.Repeat("Content line with enough characters to pass threshold. ", 8)
os.WriteFile("docs/tasks/SESSION-STATE.md", []byte(sessionContent), 0644)

	report = assessProjectMaturity()
	if report.Score < 70 {
		t.Errorf("expected score >= 70 for full setup, got %d", report.Score)
	}
	if report.Level != 3 {
		t.Errorf("expected Level 3 for score >= 70, got Level %d", report.Level)
	}
	if report.LevelName != "严格" {
		t.Errorf("expected '严格' for Level 3, got %s", report.LevelName)
	}

	// Verify hooks for Level 3
	expectedHooks := []string{"env-protect", "check-read-before-write", "check-design-doc", "check-scope", "track-read", "clear-read"}
	for _, h := range expectedHooks {
		found := false
		for _, rh := range report.Hooks {
			if rh == h {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("expected hook %s in Level 3, not found", h)
		}
	}
	if len(report.DisabledHooks) != 0 {
		t.Errorf("expected no disabled hooks for Level 3, got %v", report.DisabledHooks)
	}
}

func TestCheckTestPresenceJSAndPython(t *testing.T) {
	tmpDir := t.TempDir()
	origWd, _ := os.Getwd()
	os.Chdir(tmpDir)
	defer os.Chdir(origWd)

	srcDir := filepath.Join(tmpDir, "src")
	os.MkdirAll(srcDir, 0755)

	// JS tests (suffix .test.js, .spec.ts)
	os.WriteFile(filepath.Join(srcDir, "app.test.js"), []byte("test()"), 0644)
	os.WriteFile(filepath.Join(srcDir, "lib.spec.ts"), []byte("test()"), 0644)
	os.WriteFile(filepath.Join(srcDir, "another.test.ts"), []byte("test()"), 0644)
	os.WriteFile(filepath.Join(srcDir, "helper.spec.js"), []byte("test()"), 0644)

	d := checkTestPresence()
	if d.Score != 15 {
		t.Errorf("expected 15 score with 3+ test files, got %d", d.Score)
	}
}

func TestLevelDetermination(t *testing.T) {
	tmpDir := t.TempDir()
	origWd, _ := os.Getwd()
	os.Chdir(tmpDir)
	defer os.Chdir(origWd)

	// Test Level 2 (40-69 points)
	// Add test files for max test score (25)
	srcDir := filepath.Join(tmpDir, "src")
	os.MkdirAll(srcDir, 0755)
	for i := 0; i < 10; i++ {
		os.WriteFile(filepath.Join(srcDir, fmt.Sprintf("file%d_test.go", i)), []byte("package main"), 0644)
	}

	os.WriteFile("go.mod", []byte("module test"), 0644)
	// README > 200 chars
	readmeContent := "# Real Project\n\n" + strings.Repeat("Content line with enough characters to pass threshold. ", 8)
	os.WriteFile("README.md", []byte(readmeContent), 0644)
	os.MkdirAll("docs/project", 0755)
	os.WriteFile("docs/project/GOALS.md", []byte("# Goals\n\nReal content"), 0644)
	os.WriteFile("docs/project/ADR/ADR-001-scope.md", []byte("# ADR-001\n\n状态: 已接受"), 0644)
	os.WriteFile("docs/project/ADR/ADR-002-tech.md", []byte("# ADR-002\n\n状态: 已接受"), 0644)
	os.MkdirAll("docs/architecture", 0755)
	// ARCHITECTURE > 500 chars
	archContent := "# Architecture\n\n" + strings.Repeat("Real content with enough characters to pass the 500 character threshold for architecture documentation. ", 8)
	os.WriteFile("docs/architecture/ARCHITECTURE.md", []byte(archContent), 0644)
	os.MkdirAll("docs/architecture/modules", 0755)
	os.WriteFile("docs/architecture/modules/module1.md", []byte("# Module 1"), 0644)
	// Lint config
	os.WriteFile(".golangci.yml", []byte("linters: {}"), 0644)
	// CI
	os.MkdirAll(".github/workflows", 0755)
	os.WriteFile(".github/workflows/ci.yml", []byte("name: CI"), 0644)
	// .gitignore
	os.WriteFile(".gitignore", []byte("*.exe"), 0644)
	os.MkdirAll("docs/tasks", 0755)
	os.WriteFile("docs/tasks/TASKS.md", []byte("| T-001 | task | ✅ |\n| T-002 | task | 🔄 |\n| T-003 | task | ⬜ |"), 0644)
	// SESSION-STATE > 200 chars, no placeholder
	sessionContent := "# Session State\n\nReal content here with substantial text that exceeds 200 characters. " + strings.Repeat("Content line with enough characters to pass threshold. ", 8)
	os.WriteFile("docs/tasks/SESSION-STATE.md", []byte(sessionContent), 0644)

	report := assessProjectMaturity()
	// With full setup we get Level 3 (score >= 70)
	if report.Level != 3 {
		t.Errorf("expected Level 3 for score >= 70, got Level %d (score: %d)", report.Level, report.Score)
	}
	if report.LevelName != "严格" {
		t.Errorf("expected '严格' for Level 3, got %s", report.LevelName)
	}

	// Level 3 should have all hooks enabled
	expectedHooks := []string{"env-protect", "check-read-before-write", "check-design-doc", "check-scope", "track-read", "clear-read"}
	for _, h := range expectedHooks {
		found := false
		for _, rh := range report.Hooks {
			if rh == h {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("expected hook %s in Level 3, not found", h)
		}
	}
	if len(report.DisabledHooks) != 0 {
		t.Errorf("expected no disabled hooks for Level 3, got %v", report.DisabledHooks)
	}
}
func TestCheckTestPresenceMultiRoot(t *testing.T) {
	dir := chdirTemp(t)
	// Standard Go layout: internal/ + cmd/ + repo-root test file.
	writeRelFile(t, dir, "internal/foo/foo_test.go", "package foo")
	writeRelFile(t, dir, "cmd/bar/main_test.go", "package main")
	writeRelFile(t, dir, "main_test.go", "package main")

	d := checkTestPresence()
	if !d.Found || d.Score == 0 {
		t.Errorf("multi-root tests should be detected, got score=%d found=%v note=%q", d.Score, d.Found, d.Note)
	}
}

func TestCheckTestPresenceMultiRootEmpty(t *testing.T) {
	chdirTemp(t)
	d := checkTestPresence()
	if d.Found {
		t.Errorf("no tests anywhere should yield Found=false, got %+v", d)
	}
}

func TestCountPythonTests(t *testing.T) {
	dir := chdirTemp(t)
	writeRelFile(t, dir, "src/orders/test_orders.py", "def test_x(): pass")
	writeRelFile(t, dir, "tests/test_api.py", "def test_y(): pass")
	writeRelFile(t, dir, "src/util_helper.py", "def helper(): pass")
	writeRelFile(t, dir, "src/orders/helpers_test.py", "def test_z(): pass")

	if n := countPythonTests([]string{"src", "tests"}); n != 3 {
		t.Errorf("countPythonTests = %d, want 3 (test_*.py x2 + *_test.py x1)", n)
	}
}
