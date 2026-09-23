package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestGenerateMutantID(t *testing.T) {
	tests := []struct {
		name     string
		file     string
		line     int
		op       string
		expected string // just check format
	}{
		{
			name:     "basic",
			file:     "src/main.go",
			line:     10,
			op:       "AOR1",
			expected: "M-",
		},
		{
			name:     "different file",
			file:     "lib/helper.go",
			line:     42,
			op:       "ROR1",
			expected: "M-",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := generateMutantID(tt.file, tt.line, tt.op)
			if !strings.HasPrefix(result, tt.expected) {
				t.Errorf("generateMutantID() = %s, expected prefix %s", result, tt.expected)
			}
			// ID should be consistent for same inputs
			result2 := generateMutantID(tt.file, tt.line, tt.op)
			if result != result2 {
				t.Errorf("generateMutantID() not deterministic: %s vs %s", result, result2)
			}
		})
	}
}

func TestMutationOperators(t *testing.T) {
	// Verify operators have required fields
	for _, op := range mutationOperators {
		if op.Name == "" {
			t.Error("operator missing Name")
		}
		if op.Pattern == "" {
			t.Errorf("operator %s missing Pattern", op.Name)
		}
		if op.Replacement == "" {
			t.Errorf("operator %s missing Replacement", op.Name)
		}
		if op.Lang == "" {
			t.Errorf("operator %s missing Lang", op.Name)
		}
	}

	// Count operators by language
	langCounts := make(map[string]int)
	for _, op := range mutationOperators {
		langCounts[op.Lang]++
	}
	if langCounts["all"] == 0 {
		t.Error("expected at least one 'all' language operator")
	}
}

func TestGenerateMutantsBasic(t *testing.T) {
	tmpDir := t.TempDir()
	origWd, _ := os.Getwd()
	os.Chdir(tmpDir)
	defer os.Chdir(origWd)

	// Create a test source file
	srcDir := filepath.Join(tmpDir, "src")
	os.MkdirAll(srcDir, 0755)
	sourceFile := filepath.Join(srcDir, "main.go")
	content := `package main

func add(a, b int) int {
	return a + b
}

func compare(x, y int) bool {
	return x > y
}

func logical(a, b bool) bool {
	return a && b
}
`
	os.WriteFile(sourceFile, []byte(content), 0644)

	// Generate mutants for Go
	mutants := generateMutants([]string{sourceFile}, 10, "go")

	// Should generate at least some mutants
	if len(mutants) == 0 {
		t.Error("expected at least 1 mutant")
	}

	// Each mutant should have valid fields
	for _, m := range mutants {
		if m.ID == "" {
			t.Error("mutant missing ID")
		}
		if m.File == "" {
			t.Error("mutant missing File")
		}
		if m.Line <= 0 {
			t.Error("mutant Line should be > 0")
		}
		if m.Original == "" {
			t.Error("mutant missing Original")
		}
		if m.Mutated == "" {
			t.Error("mutant missing Mutated")
		}
		if m.Type == "" {
			t.Error("mutant missing Type")
		}
		if m.Original == m.Mutated {
			t.Error("mutant Original and Mutated should differ")
		}
	}
}

func TestGenerateMutantsLanguageFilter(t *testing.T) {
	tmpDir := t.TempDir()
	origWd, _ := os.Getwd()
	os.Chdir(tmpDir)
	defer os.Chdir(origWd)

	srcDir := filepath.Join(tmpDir, "src")
	os.MkdirAll(srcDir, 0755)

	// Go file with Go-specific return pattern (must match "return true" pattern)
	goFile := filepath.Join(srcDir, "main.go")
	goContent := `package main

func test() bool {
	return true
}
`
	os.WriteFile(goFile, []byte(goContent), 0644)

	// JS file with JS-specific return pattern
	jsDir := filepath.Join(tmpDir, "src")
	os.MkdirAll(jsDir, 0755)
	jsFile := filepath.Join(jsDir, "app.js")
	jsContent := `function test() {
return true;
}
`
	os.WriteFile(jsFile, []byte(jsContent), 0644)

	// Test Go language filtering
	goMutants := generateMutants([]string{goFile}, 10, "go")
	for _, m := range goMutants {
		if strings.HasSuffix(m.Type, "_JS") || strings.HasSuffix(m.Type, "_PY") || strings.HasSuffix(m.Type, "_RS") {
			t.Errorf("found non-Go operator %s in Go project", m.Type)
		}
	}

	// Test JS language filtering
	jsMutants := generateMutants([]string{jsFile}, 10, "node")
	for _, m := range jsMutants {
		if strings.HasSuffix(m.Type, "_GO") || strings.HasSuffix(m.Type, "_PY") || strings.HasSuffix(m.Type, "_RS") {
			t.Errorf("found non-JS operator %s in Node project", m.Type)
		}
	}
}

func TestGenerateMutantsMaxLimit(t *testing.T) {
	tmpDir := t.TempDir()
	origWd, _ := os.Getwd()
	os.Chdir(tmpDir)
	defer os.Chdir(origWd)

	srcDir := filepath.Join(tmpDir, "src")
	os.MkdirAll(srcDir, 0755)

	// Create a file with many mutation opportunities
	var content strings.Builder
	content.WriteString("package main\n")
	for i := 0; i < 50; i++ {
		content.WriteString(fmt.Sprintf("func f%d() int { return %d + %d }\n", i, i, i+1))
	}
	sourceFile := filepath.Join(srcDir, "many.go")
	os.WriteFile(sourceFile, []byte(content.String()), 0644)

	mutants := generateMutants([]string{sourceFile}, 5, "go")
	if len(mutants) > 5 {
		t.Errorf("expected at most 5 mutants with maxMutants=5, got %d", len(mutants))
	}
}

func TestApplyRevertMutation(t *testing.T) {
	tmpDir := t.TempDir()
	origWd, _ := os.Getwd()
	os.Chdir(tmpDir)
	defer os.Chdir(origWd)

	srcDir := filepath.Join(tmpDir, "src")
	os.MkdirAll(srcDir, 0755)
	sourceFile := filepath.Join(srcDir, "main.go")
	originalContent := `package main

func add(a, b int) int {
	return a + b
}
`
	os.WriteFile(sourceFile, []byte(originalContent), 0644)

	// Create a mutant
	mutant := &Mutant{
		ID:       "M-test",
		File:     sourceFile,
		Line:     4,
		Original: "	return a + b",
		Mutated:  "	return a - b",
		Type:     "AOR1",
	}

	// Apply
	applyMutation(mutant)

	// Verify mutation applied
	data, _ := os.ReadFile(sourceFile)
	if !strings.Contains(string(data), "a - b") {
		t.Errorf("mutation not applied: %s", string(data))
	}

	// Revert
	revertMutation(mutant)

	// Verify reverted
	data, _ = os.ReadFile(sourceFile)
	if !strings.Contains(string(data), "a + b") {
		t.Errorf("mutation not reverted: %s", string(data))
	}
}

func TestRunExistingTestsForMutationUnknown(t *testing.T) {
	// Unknown project type should return true (skip)
	result := runExistingTestsForMutation("unknown")
	if !result {
		t.Error("unknown project type should return true (skip)")
	}
}

func TestSaveMutationReport(t *testing.T) {
	tmpDir := t.TempDir()
	origWd, _ := os.Getwd()
	os.Chdir(tmpDir)
	defer os.Chdir(origWd)

	report := MutationReport{
		TotalMutants:    10,
		KilledMutants:   8,
		SurvivedMutants: 2,
		MutationScore:   80.0,
		Duration:        "1.5s",
		Passed:          true,
		ProjectType:     "go",
		Mutants: []Mutant{
			{ID: "M-1", File: "src/main.go", Line: 10, Type: "AOR1", Killed: true},
			{ID: "M-2", File: "src/main.go", Line: 15, Type: "ROR1", Killed: false},
		},
	}

	saveMutationReport(report)

	// Verify file created
	reportFile := filepath.Join(".claude", "metrics", "mutation-report.json")
	if _, err := os.Stat(reportFile); err != nil {
		t.Errorf("report file not created: %v", err)
	}
}

func TestPrintMutationReport(t *testing.T) {
	// Just verify it doesn't panic
	report := MutationReport{
		TotalMutants:    5,
		KilledMutants:   4,
		SurvivedMutants: 1,
		MutationScore:   80.0,
		Duration:        "1s",
		Passed:          true,
		ProjectType:     "go",
	}
	printMutationReport(report, false)
	printMutationReport(report, true)
}

func TestFindSourceFiles(t *testing.T) {
	tmpDir := t.TempDir()
	origWd, _ := os.Getwd()
	os.Chdir(tmpDir)
	defer os.Chdir(origWd)

	// Test with Go project
	os.MkdirAll("src", 0755)
	os.WriteFile("src/main.go", []byte("package main"), 0644)
	os.WriteFile("src/helper.go", []byte("package main"), 0644)

	files := findSourceFiles("go")
	if len(files) < 2 {
		t.Errorf("expected at least 2 Go files, got %d", len(files))
	}

	// Test with unknown type
	unknownFiles := findSourceFiles("unknown")
	if unknownFiles != nil {
		t.Error("unknown project type should return nil")
	}
}