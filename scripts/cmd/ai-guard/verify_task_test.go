package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)


func TestGetChangedFiles(t *testing.T) {
	// In a git repo, this should work
	files := getChangedFiles()
	// Could be nil if no changes or no git
	if files == nil {
		// OK
		return
	}
	// Each file should be a non-empty string
	for _, f := range files {
		if f == "" {
			t.Error("getChangedFiles() returned empty string")
		}
	}
}

func TestGetChangedPackages(t *testing.T) {
	files := []string{
		"src/main.go",
		"src/lib/helper.go",
		"docs/readme.md",
		"src/lib/util.go",
	}
	packages := getChangedPackages(files, ".go")

	// Should dedupe src/lib
	if len(packages) != 2 {
		t.Errorf("expected 2 unique packages, got %d: %v", len(packages), packages)
	}
}

func TestFilterByExt(t *testing.T) {
	files := []string{
		"main.go",
		"test.py",
		"index.js",
		"main_test.go",
	}

	goFiles := filterByExt(files, ".go")
	if len(goFiles) != 2 {
		t.Errorf("expected 2 .go files, got %d", len(goFiles))
	}

	pyFiles := filterByExt(files, ".py")
	if len(pyFiles) != 1 {
		t.Errorf("expected 1 .py file, got %d", len(pyFiles))
	}
}

func TestCheckVerifySecrets(t *testing.T) {
	dir := t.TempDir()
	origWd, _ := os.Getwd()
	os.Chdir(dir)
	defer os.Chdir(origWd)

	// No source dir → no findings.
	if n := checkVerifySecrets(); n != 0 {
		t.Errorf("no source should yield 0 findings, got %d", n)
	}
	// Source with a hardcoded password → findings > 0.
	writeRelFile(t, dir, "src/secret.go", "package main\nvar cfg = \"password = \"hunter2\"\"\n")
	if n := checkVerifySecrets(); n == 0 {
		t.Error("hardcoded password in src/ should be detected")
	}
}

func TestUpdateTasksStatus(t *testing.T) {
	tmpDir := t.TempDir()
	origWd, _ := os.Getwd()
	os.Chdir(tmpDir)
	defer os.Chdir(origWd)

	// Create tasks file
	tasksContent := `| T-001 | task one | P0 | 🔄 | dep | note |
| T-002 | task two | P0 | ⬜ | dep | note |
`
	tasksFile := "docs/tasks/TASKS.md"
	os.MkdirAll(filepath.Dir(tasksFile), 0755)
	os.WriteFile(tasksFile, []byte(tasksContent), 0644)

	// Update T-001 to completed
	updateTasksStatus("T-001")

	// Verify
	content, _ := os.ReadFile(tasksFile)
	if !contains(string(content), "T-001") {
		t.Fatal("T-001 should still be in file")
	}
	// T-001 must be flipped 🔄 → ✅; T-002 untouched (still ⬜).
	line1 := findTaskLine(string(content), "T-001")
	if !strings.Contains(line1, "✅") || strings.Contains(line1, "🔄") {
		t.Errorf("T-001 should be ✅ after update, got: %s", line1)
	}
	line2 := findTaskLine(string(content), "T-002")
	if !strings.Contains(line2, "⬜") {
		t.Errorf("T-002 should stay ⬜, got: %s", line2)
	}
}

func TestUpdateSessionState(t *testing.T) {
	tmpDir := t.TempDir()
	origWd, _ := os.Getwd()
	os.Chdir(tmpDir)
	defer os.Chdir(origWd)

	// Create session state file
	sessionContent := `# 会话状态

| 字段 | 值 |
|------|------|
| **会话日期** | YYYY-MM-DD |
| **执行任务** | T-XXX |
| **完成状态** | 🔄 |
| **会话摘要** | （待补充） |
`
	sessionFile := "docs/tasks/SESSION-STATE.md"
	os.MkdirAll(filepath.Dir(sessionFile), 0755)
	os.WriteFile(sessionFile, []byte(sessionContent), 0644)

	updateSessionState("T-100")

	content, _ := os.ReadFile(sessionFile)
	if !contains(string(content), "T-100") {
		t.Error("updateSessionState should update task ID")
	}
}

func TestUpdateSessionStateNoFile(t *testing.T) {
	tmpDir := t.TempDir()
	origWd, _ := os.Getwd()
	os.Chdir(tmpDir)
	defer os.Chdir(origWd)

	// Should not crash if file doesn't exist
	updateSessionState("T-100")
}

func TestRecordMetrics(t *testing.T) {
	tmpDir := t.TempDir()
	origWd, _ := os.Getwd()
	os.Chdir(tmpDir)
	defer os.Chdir(origWd)

	// Should create metrics file
	recordMetrics("T-100", true)

	// Verify file exists
	metricsFile := filepath.Join(".claude", "metrics", "verification.log")
	if _, err := os.Stat(metricsFile); err != nil {
		t.Errorf("verification.log should be created: %v", err)
	}

	// Test failure case
	recordMetrics("T-101", false)

	// Read the log
	content, err := os.ReadFile(metricsFile)
	if err != nil {
		t.Fatalf("failed to read metrics: %v", err)
	}
	if !contains(string(content), "T-100") {
		t.Error("log should contain T-100")
	}
	if !contains(string(content), "T-101") {
		t.Error("log should contain T-101")
	}
}

func TestUpdateRequirements(t *testing.T) {
	tmpDir := t.TempDir()
	origWd, _ := os.Getwd()
	os.Chdir(tmpDir)
	defer os.Chdir(origWd)

	// Setup
	os.MkdirAll("docs/tasks", 0755)
	tasksContent := `| T-001 | task with REQ-001 REQ-002 | ⬜ |`
	os.WriteFile("docs/tasks/TASKS.md", []byte(tasksContent), 0644)
	reqContent := `| REQ-001 | requirement one | ⬜ |
| REQ-002 | requirement two | ⬜ |
`
	os.WriteFile("docs/tasks/REQUIREMENTS.md", []byte(reqContent), 0644)

	updateRequirements("T-001")

	// Verify requirements were updated
	content, _ := os.ReadFile("docs/tasks/REQUIREMENTS.md")
	if !contains(string(content), "REQ-001") {
		t.Error("REQ-001 should still exist")
	}
	if !contains(string(content), "✅ 设计") {
		t.Errorf("REQ-001 should be flipped to ✅ 设计, got:\n%s", string(content))
	}
}

// Helper function used by tests
func contains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(s) > 0 && containsHelper(s, substr))
}

func containsHelper(s, substr string) bool {
	for i := 0; i+len(substr) <= len(s); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}

func TestRunVerifyTestsUnknownType(t *testing.T) {
	tmpDir := t.TempDir()
	origWd, _ := os.Getwd()
	os.Chdir(tmpDir)
	defer os.Chdir(origWd)

	// No project files: should return true (skip)
	result := runVerifyTests("unknown")
	if !result {
		t.Error("runVerifyTests with unknown type should return true (skip)")
	}
}

func TestCheckDocSync(t *testing.T) {
	tmpDir := t.TempDir()
	origWd, _ := os.Getwd()
	os.Chdir(tmpDir)
	defer os.Chdir(origWd)

	// Just verify it doesn't panic
	checkDocSync()
}
func TestCheckTaskEvidence(t *testing.T) {
	dir := chdirTemp(t)
	// Unchecked acceptance criteria → blocks (false).
	writeRelFile(t, dir, "docs/tasks/T-101.md", `## 验收标准
- [ ] AC1: 能登录
- [x] AC2: 能退出
`)
	if checkTaskEvidence("T-101") {
		t.Error("unchecked acceptance criteria should block (return false)")
	}

	// All checked → passes (true).
	writeRelFile(t, dir, "docs/tasks/T-101.md", `## 验收标准
- [x] AC1: 能登录
- [x] AC2: 能退出
`)
	if !checkTaskEvidence("T-101") {
		t.Error("all-checked acceptance criteria should pass")
	}

	// Missing task file → skip (true).
	if !checkTaskEvidence("T-999") {
		t.Error("missing task file should skip (return true)")
	}

	// No checkbox items in acceptance section → skip (true).
	writeRelFile(t, dir, "docs/tasks/T-102.md", `## 验收标准
一些文字描述
`)
	if !checkTaskEvidence("T-102") {
		t.Error("no checkbox items should skip (return true)")
	}
}

func TestCheckMatrixCoverage(t *testing.T) {
	dir := chdirTemp(t)
	// No matrix file → false.
	if checkMatrixCoverage() {
		t.Error("missing matrix should report no empty cells")
	}
	// Matrix with empty cell → true.
	writeRelFile(t, dir, "docs/quality/TEST-MATRIX.md", "| # | 功能 | 正常 |\n| 1 | login | ⬜ |\n")
	if !checkMatrixCoverage() {
		t.Error("matrix with ⬜ cell should report uncovered")
	}
	// Fully covered matrix → false.
	writeRelFile(t, dir, "docs/quality/TEST-MATRIX.md", "| # | 功能 | 正常 |\n| 1 | login | ✅ |\n")
	if checkMatrixCoverage() {
		t.Error("fully covered matrix should report no empty cells")
	}
}

func TestCheckTaskEvidenceSubheadings(t *testing.T) {
	dir := chdirTemp(t)
	// Template's recommended format has ### sub-headings under 验收标准.
	// Unchecked ACs there must still block (previous bug truncated the section).
	writeRelFile(t, dir, "docs/tasks/T-101.md", `## 验收标准

### 功能验收

- [ ] AC1: 能登录
- [ ] AC2: 能退出

### 技术验收

- [ ] 编译通过
`)
	if checkTaskEvidence("T-101") {
		t.Error("unchecked ACs under ### sub-headings should block (return false)")
	}

	// All checked → passes.
	writeRelFile(t, dir, "docs/tasks/T-101.md", `## 验收标准

### 功能验收

- [x] AC1: 能登录

### 技术验收

- [x] 编译通过
`)
	if !checkTaskEvidence("T-101") {
		t.Error("all-checked ACs should pass")
	}
}

func TestCheckTaskEvidenceUppercaseCheckbox(t *testing.T) {
	dir := chdirTemp(t)
	// Uppercase [X] must count as checked, not as "no checkbox".
	writeRelFile(t, dir, "docs/tasks/T-101.md", "## 验收标准\n- [X] AC1: 能登录\n")
	if !checkTaskEvidence("T-101") {
		t.Error("uppercase [X] should count as checked")
	}
}
