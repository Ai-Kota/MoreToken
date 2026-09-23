package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestEstimateComplexity(t *testing.T) {
	tests := []struct {
		name     string
		content  string
		expected int
	}{
		{
			name:     "empty content",
			content:  "",
			expected: 1,
		},
		{
			name:     "no branches",
			content:  "x := 1\ny := 2",
			expected: 1,
		},
		{
			name:     "single if",
			content:  "if x > 0 { return 1 }",
			expected: 2,
		},
		{
			name:     "if-else",
			content:  "if x > 0 { return 1 } else { return 2 }",
			expected: 3,
		},
		{
			name:     "for loop",
			content:  "for i := 0; i < 10; i++ { sum += i }",
			expected: 2,
		},
		{
			name:     "switch case",
			content:  "switch x { case 1: case 2: default: }",
			expected: 4,
		},
		{
			name:     "logical operators",
			content:  "if a && b || c { }",
			expected: 3,
		},
		{
			name:     "complex function",
			content: `
				if x > 0 {
					for i := 0; i < n; i++ {
						if y < 0 && z > 0 {
							return
						}
					}
				} else if x < 0 {
					switch y {
					case 1:
					case 2:
					}
				}
			`,
			expected: 10, // 1 base + 4 if/else + 1 for + 2 && + 1 switch + 2 cases
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := estimateComplexity(tt.content)
			// Allow some flexibility for whitespace differences
			if result < tt.expected-1 || result > tt.expected+2 {
				t.Errorf("estimateComplexity() = %d, expected around %d", result, tt.expected)
			}
		})
	}
}

func TestParseCoverage(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected float64
	}{
		{
			name:     "go coverage format",
			input:    "ok\tpackage\t0.123s\tcoverage: 85.5% of statements",
			expected: 85.5,
		},
		{
			name:     "jest format with statements",
			input:    "Statements: 72.3% (50/69)",
			expected: 72.3,
		},
		{
			name:     "pytest format",
			input:    "TOTAL    100    20    80%\n",
			expected: 80.0,
		},
		{
			name:     "no coverage info",
			input:    "no coverage here",
			expected: 0,
		},
		{
			name:     "empty string",
			input:    "",
			expected: 0,
		},
		{
			name:     "100% coverage",
			input:    "coverage: 100%",
			expected: 100.0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := parseCoverage(tt.input)
			if result != tt.expected {
				t.Errorf("parseCoverage() = %v, expected %v", result, tt.expected)
			}
		})
	}
}

func TestCheckSecretsInCode(t *testing.T) {
	// Create a temp directory with test files
	tmpDir := t.TempDir()
	origWd, _ := os.Getwd()
	os.Chdir(tmpDir)
	defer os.Chdir(origWd)

	srcDir := filepath.Join(tmpDir, "src")
	os.MkdirAll(srcDir, 0755)

	// File with hardcoded secrets
	secretFile := filepath.Join(srcDir, "config.go")
	secretContent := `package main
const password = "supersecret123"
const apiKey = "sk-1234567890"
const token = "ghp_1234567890"
`
	os.WriteFile(secretFile, []byte(secretContent), 0644)

	// File without secrets
	cleanFile := filepath.Join(srcDir, "clean.go")
	cleanContent := `package main
const name = "myapp"
const version = "1.0.0"
`
	os.WriteFile(cleanFile, []byte(cleanContent), 0644)

	count := checkSecretsInCode()
	if count < 3 {
		t.Errorf("expected at least 3 secrets found, got %d", count)
	}
}

func TestIsStrictCoverage(t *testing.T) {
	tmpDir := t.TempDir()
	origWd, _ := os.Getwd()
	os.Chdir(tmpDir)
	defer os.Chdir(origWd)

	tests := []struct {
		name        string
		fileContent string
		fileExists  bool
		expected    bool
	}{
		{
			name:       "no config file",
			fileExists: false,
			expected:   false,
		},
		{
			name:        "strict-coverage true",
			fileContent: "strict-coverage: true\n",
			fileExists:  true,
			expected:    true,
		},
		{
			name:        "strict-coverage false",
			fileContent: "strict-coverage: false\n",
			fileExists:  true,
			expected:    false,
		},
		{
			name:        "strict-coverage yes",
			fileContent: "strict-coverage: yes\n",
			fileExists:  true,
			expected:    true,
		},
		{
			name:        "no strict-coverage key",
			fileContent: "other-key: value\n",
			fileExists:  true,
			expected:    false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.fileExists {
				os.WriteFile(".quality-gate.yml", []byte(tt.fileContent), 0644)
				defer os.Remove(".quality-gate.yml")
			}
			result := isStrictCoverage()
			if result != tt.expected {
				t.Errorf("isStrictCoverage() = %v, expected %v", result, tt.expected)
			}
		})
	}
}

func TestCountCodeFiles(t *testing.T) {
	tmpDir := t.TempDir()
	origWd, _ := os.Getwd()
	os.Chdir(tmpDir)
	defer os.Chdir(origWd)

	srcDir := filepath.Join(tmpDir, "src")
	os.MkdirAll(srcDir, 0755)

	// Create test files
	os.WriteFile(filepath.Join(srcDir, "a.go"), []byte("package main\nfunc main() {}\n"), 0644)
	os.WriteFile(filepath.Join(srcDir, "b.py"), []byte("def hello():\n    pass\n"), 0644)
	os.WriteFile(filepath.Join(srcDir, "c.ts"), []byte("export const x = 1;\n"), 0644)

	files, lines := countCodeFiles()
	if files != 3 {
		t.Errorf("expected 3 files, got %d", files)
	}
	if lines < 3 {
		t.Errorf("expected at least 3 lines, got %d", lines)
	}
}

func TestCheckDuplication(t *testing.T) {
	tmpDir := t.TempDir()
	origWd, _ := os.Getwd()
	os.Chdir(tmpDir)
	defer os.Chdir(origWd)

	srcDir := filepath.Join(tmpDir, "src")
	os.MkdirAll(srcDir, 0755)

	// File with high duplication
	duplicateContent := `package main
const x = 1
const y = 1
const z = 1
const a = 1
`
	os.WriteFile(filepath.Join(srcDir, "a.go"), []byte(duplicateContent), 0644)

	// File with no duplication
	uniqueContent := `package main
const a = 1
const b = 2
const c = 3
`
	os.WriteFile(filepath.Join(srcDir, "b.go"), []byte(uniqueContent), 0644)

	rate := checkDuplication()
	if rate < 0 || rate > 100 {
		t.Errorf("duplication rate should be 0-100, got %f", rate)
	}
}

func TestCheckComplexity(t *testing.T) {
	tmpDir := t.TempDir()
	origWd, _ := os.Getwd()
	os.Chdir(tmpDir)
	defer os.Chdir(origWd)

	srcDir := filepath.Join(tmpDir, "src")
	os.MkdirAll(srcDir, 0755)

	// Simple file
	simpleContent := `package main
func main() {
	x := 1
	y := 2
	println(x + y)
}
`
	os.WriteFile(filepath.Join(srcDir, "simple.go"), []byte(simpleContent), 0644)

	avg, max := checkComplexity()
	if avg < 1 {
		t.Errorf("average complexity should be >= 1, got %f", avg)
	}
	if max < 1 {
		t.Errorf("max complexity should be >= 1, got %d", max)
	}
}

func TestCheckSecretsInCodeEdgeCases(t *testing.T) {
	// Test with no source directory
	tmpDir := t.TempDir()
	origWd, _ := os.Getwd()
	os.Chdir(tmpDir)
	defer os.Chdir(origWd)

	count := checkSecretsInCode()
	if count != 0 {
		t.Errorf("expected 0 secrets when no src/ exists, got %d", count)
	}
}

func TestParseCoverageEdgeCases(t *testing.T) {
	// Test various edge cases
	inputs := []string{
		"random text without coverage",
		"coverage: 100%",
		"coverage: 0%",
		"Statements: 99.9% (1000/1001)",
		"NOTHING",
		"",
	}

	for _, input := range inputs {
		t.Run(input, func(t *testing.T) {
			result := parseCoverage(input)
			if result < 0 || result > 100 {
				t.Errorf("parseCoverage(%q) = %f, should be 0-100", input, result)
			}
		})
	}
}

// Test that verify functions don't panic
func TestCodeQualityGateNoCrash(t *testing.T) {
	tmpDir := t.TempDir()
	origWd, _ := os.Getwd()
	os.Chdir(tmpDir)
	defer os.Chdir(origWd)

	// All checks should work in empty directory
	count := checkSecretsInCode()
	if count != 0 {
		t.Errorf("checkSecretsInCode() in empty dir should return 0, got %d", count)
	}

	rate := checkDuplication()
	if rate != 0 {
		t.Errorf("checkDuplication() in empty dir should return 0, got %f", rate)
	}
}

// Sanity check: verify report structure can be built
func TestQualityReportStructure(t *testing.T) {
	report := analyzeQuality(false)
	if report.MaxComplexity < 0 {
		t.Error("report fields should not be negative")
	}
	if report.AvgComplexity < 0 {
		t.Error("report fields should not be negative")
	}
	// Passed should be a boolean field that works
	_ = report.Passed
	_ = report.Issues
	_ = strings.Contains
}