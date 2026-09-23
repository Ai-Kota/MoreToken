package main

import (
	"os"
	"strings"
	"testing"
)

func TestExtractFeatures(t *testing.T) {
	req := `# 需求
功能：用户注册
Feature: User Login
需求：密码重置
- 数据导出
• 报表生成
1. 权限控制

无标记短行也算功能
长行含冒号：这种不应被无标记分支重复添加`

	features := extractFeatures(req)

	for _, want := range []string{"用户注册", "User Login", "密码重置", "数据导出", "报表生成", "权限控制"} {
		if !containsStr(features, want) {
			t.Errorf("extractFeatures missing %q, got %v", want, features)
		}
	}
	// Dedup check: marker line shouldn't be double-added by the fallback.
	if len(features) != len(uniqueStrings(features)) {
		t.Errorf("extractFeatures should dedupe, got %v", features)
	}
}

func TestExtractFeaturesEmpty(t *testing.T) {
	if f := extractFeatures(""); len(f) != 0 {
		t.Errorf("empty req = %v, want empty", f)
	}
}

func TestGenerateInput(t *testing.T) {
	tests := map[string]string{
		"normal":   "valid input for 用户注册",
		"edge":     "empty string, zero value, maximum length",
		"negative": "invalid input, malformed data, missing fields",
		"boundary": "just below/above threshold, empty array, single element",
	}
	for typ, want := range tests {
		if got := generateInput("用户注册", typ); got != want {
			t.Errorf("generateInput(%s) = %q, want %q", typ, got, want)
		}
	}
	if got := generateInput("x", "unknown"); got != "generic input" {
		t.Errorf("unknown type = %q", got)
	}
}

func TestGenerateExpected(t *testing.T) {
	if got := generateExpected("f", "normal"); !strings.Contains(got, "successful") {
		t.Errorf("normal expected = %q", got)
	}
	if got := generateExpected("f", "edge"); !strings.Contains(got, "graceful") {
		t.Errorf("edge expected = %q", got)
	}
	if got := generateExpected("f", "negative"); !strings.Contains(got, "error") {
		t.Errorf("negative expected = %q", got)
	}
	if got := generateExpected("f", "boundary"); !strings.Contains(got, "boundary") {
		t.Errorf("boundary expected = %q", got)
	}
}

func TestGenerateEnhancedTestSuite(t *testing.T) {
	suite := generateEnhancedTestSuite("功能：用户登录\n功能：密码修改", "req.md")
	// 2 features × 4 cases each = 8
	if suite.TotalTests != 8 {
		t.Errorf("TotalTests = %d, want 8", suite.TotalTests)
	}
	if suite.Requirement != "req.md" {
		t.Errorf("Requirement = %q", suite.Requirement)
	}
	// Each feature produces normal/edge/negative/boundary.
	types := map[string]bool{}
	for _, tc := range suite.TestCases {
		types[tc.Type] = true
	}
	for _, want := range []string{"unit", "edge", "negative"} {
		if !types[want] {
			t.Errorf("suite missing type %q", want)
		}
	}
}

func TestSanitizeName(t *testing.T) {
	tests := []struct{ in, want string }{
		{"User Login", "user_login"},
		{"a/b/c", "a_b_c"},
		{"123", "123"},
	}
	for _, tt := range tests {
		if got := sanitizeName(tt.in); got != tt.want {
			t.Errorf("sanitizeName(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
	long := strings.Repeat("x", 100)
	if got := sanitizeName(long); len(got) > 50 {
		t.Errorf("sanitizeName should cap at 50, got %d", len(got))
	}
}

func TestWriteEnhancedGoTests(t *testing.T) {
	dir := chdirTemp(t)
	suite := generateEnhancedTestSuite("功能：用户注册", "req.md")
	writeEnhancedGoTests(suite, dir)
	data, err := os.ReadFile("generated_test.go")
	if err != nil {
		t.Fatal(err)
	}
	content := string(data)
	if !strings.Contains(content, "package tests") || !strings.Contains(content, "func Test_") {
		t.Errorf("generated Go test malformed:\n%s", content)
	}
}

func TestRunTestDesigner(t *testing.T) {
	dir := chdirTemp(t)
	writeRelFile(t, dir, "go.mod", "module demo\n\ngo 1.21\n")
	writeRelFile(t, dir, "requirements.md", "功能：用户注册\n功能：登录")

	code := runWithExitCapture(t, func() {
		runTestDesigner([]string{"requirements.md", "--output", "tests/generated"})
	})
	if code == 1 {
		t.Errorf("test-designer should not fail, got exit %d", code)
	}
	if !fileExists("tests/generated/test-suite.json") {
		t.Error("test-suite.json should be generated")
	}
	if !fileExists("tests/generated/generated_test.go") {
		t.Error("generated_test.go should be generated for Go project")
	}
}

func TestWriteEnhancedNodeTests(t *testing.T) {
	dir := chdirTemp(t)
	suite := generateEnhancedTestSuite("功能：用户登录", "req.md")
	writeEnhancedNodeTests(suite, dir)
	data, err := os.ReadFile("generated.test.js")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "describe(") {
		t.Errorf("generated Node test malformed:\n%s", string(data))
	}
}

func TestWriteEnhancedPythonTests(t *testing.T) {
	dir := chdirTemp(t)
	suite := generateEnhancedTestSuite("功能：用户登录", "req.md")
	writeEnhancedPythonTests(suite, dir)
	data, err := os.ReadFile("test_generated.py")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "def test_") || !strings.Contains(string(data), "import pytest") {
		t.Errorf("generated Python test malformed:\n%s", string(data))
	}
}

func TestRunTestDesignerMissingFile(t *testing.T) {
	chdirTemp(t)
	code := runWithExitCapture(t, func() {
		runTestDesigner([]string{"nonexistent.md"})
	})
	if code != 1 {
		t.Errorf("missing requirement file should exit 1, got %d", code)
	}
}

func containsStr(slice []string, s string) bool {
	for _, v := range slice {
		if v == s {
			return true
		}
	}
	return false
}

func uniqueStrings(s []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, v := range s {
		if !seen[v] {
			seen[v] = true
			out = append(out, v)
		}
	}
	return out
}
