// test-designer: Independent test case generation
// Generates test cases from requirements, independent of code.
// Usage: test-designer <requirement-file> [--output <dir>]
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

type TestCase struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description"`
	Input       string `json:"input"`
	Expected    string `json:"expected"`
	Type        string `json:"type"`     // unit, integration, edge, negative, performance
	Priority    string `json:"priority"` // P0, P1, P2
	Tags        []string `json:"tags"`
}

type TestSuite struct {
	Requirement string     `json:"requirement"`
	TestCases   []TestCase `json:"test_cases"`
	TotalTests  int        `json:"total_tests"`
}

func runTestDesigner(args []string) {
	if len(args) < 1 {
		fmt.Println("Usage: ai-guard test-designer <requirement-file> [--output <dir>]")
		fmt.Println()
		fmt.Println("Generates test cases from a requirement document.")
		fmt.Println("The test cases are independent of the code implementation.")
		osExit(1)
	}

	reqFile := args[0]
	outputDir := "tests/generated"
	for i, arg := range args {
		if arg == "--output" && i+1 < len(args) {
			outputDir = args[i+1]
		}
	}

	reqContent, err := os.ReadFile(reqFile)
	if err != nil {
		fmt.Printf("Error reading requirement file: %v\n", err)
		osExit(1)
	}

	suite := generateEnhancedTestSuite(string(reqContent), reqFile)

	os.MkdirAll(outputDir, 0755)

	jsonFile := filepath.Join(outputDir, "test-suite.json")
	data, _ := json.MarshalIndent(suite, "", "  ")
	os.WriteFile(jsonFile, data, 0644)

	projectType := detectProjectType()
	switch projectType {
	case "go":
		writeEnhancedGoTests(suite, outputDir)
	case "node":
		writeEnhancedNodeTests(suite, outputDir)
	case "python":
		writeEnhancedPythonTests(suite, outputDir)
	}

	fmt.Printf("✅ Generated %d test cases\n", suite.TotalTests)
	fmt.Printf("   📁 %s\n", outputDir)
	fmt.Println()
	fmt.Println("Test cases by type:")
	byType := make(map[string]int)
	for _, tc := range suite.TestCases {
		byType[tc.Type]++
	}
	for t, count := range byType {
		fmt.Printf("   %s: %d\n", t, count)
	}
	fmt.Println()
	fmt.Println("Test cases by priority:")
	byPriority := make(map[string]int)
	for _, tc := range suite.TestCases {
		byPriority[tc.Priority]++
	}
	for p, count := range byPriority {
		fmt.Printf("   %s: %d\n", p, count)
	}
}

func generateEnhancedTestSuite(requirement string, source string) TestSuite {
	suite := TestSuite{
		Requirement: source,
		TestCases:   []TestCase{},
	}

	lines := strings.Split(requirement, "\n")
	testID := 1

	// Extract feature descriptions and acceptance criteria
	features := extractFeatures(requirement)
	if len(features) == 0 {
		// Fallback: treat each non-empty line as a feature
		for _, line := range lines {
			line = strings.TrimSpace(line)
			if line == "" || strings.HasPrefix(line, "#") {
				continue
			}
			features = append(features, line)
		}
	}

	for _, feature := range features {
		// Normal test
		suite.TestCases = append(suite.TestCases, TestCase{
			ID:          fmt.Sprintf("TC-%03d", testID),
			Name:        sanitizeName(feature) + "_normal",
			Description: fmt.Sprintf("Normal flow: %s", feature),
			Input:       generateInput(feature, "normal"),
			Expected:    generateExpected(feature, "normal"),
			Type:        "unit",
			Priority:    "P0",
			Tags:        []string{"happy-path"},
		})
		testID++

		// Edge case
		suite.TestCases = append(suite.TestCases, TestCase{
			ID:          fmt.Sprintf("TC-%03d", testID),
			Name:        sanitizeName(feature) + "_edge",
			Description: fmt.Sprintf("Edge case: %s", feature),
			Input:       generateInput(feature, "edge"),
			Expected:    generateExpected(feature, "edge"),
			Type:        "edge",
			Priority:    "P1",
			Tags:        []string{"edge-case"},
		})
		testID++

		// Negative test
		suite.TestCases = append(suite.TestCases, TestCase{
			ID:          fmt.Sprintf("TC-%03d", testID),
			Name:        sanitizeName(feature) + "_negative",
			Description: fmt.Sprintf("Negative test: %s", feature),
			Input:       generateInput(feature, "negative"),
			Expected:    generateExpected(feature, "negative"),
			Type:        "negative",
			Priority:    "P1",
			Tags:        []string{"error-handling"},
		})
		testID++

		// Boundary test
		suite.TestCases = append(suite.TestCases, TestCase{
			ID:          fmt.Sprintf("TC-%03d", testID),
			Name:        sanitizeName(feature) + "_boundary",
			Description: fmt.Sprintf("Boundary test: %s", feature),
			Input:       generateInput(feature, "boundary"),
			Expected:    generateExpected(feature, "boundary"),
			Type:        "edge",
			Priority:    "P2",
			Tags:        []string{"boundary"},
		})
		testID++
	}

	suite.TotalTests = len(suite.TestCases)
	return suite
}

func extractFeatures(req string) []string {
	// Look for common markers
	markers := []string{
		`功能：\s*(.+)`,
		`Feature:\s*(.+)`,
		`需求：\s*(.+)`,
		`Requirement:\s*(.+)`,
		`- (.+)`,
		`• (.+)`,
		`\d+\.\s*(.+)`,
	}
	var features []string
	lines := strings.Split(req, "\n")
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		for _, pattern := range markers {
			re := regexp.MustCompile(pattern)
			matches := re.FindStringSubmatch(trimmed)
			if len(matches) > 1 {
				features = append(features, strings.TrimSpace(matches[1]))
				break
			}
		}
		// If no marker found, but line is short and not a header, treat as feature
		if len(trimmed) < 100 && !strings.Contains(trimmed, "：") && !strings.Contains(trimmed, ":") {
			if !strings.HasPrefix(trimmed, "```") && !strings.HasPrefix(trimmed, "<!--") {
				features = append(features, trimmed)
			}
		}
	}
	// Deduplicate
	seen := make(map[string]bool)
	var unique []string
	for _, f := range features {
		if !seen[f] {
			seen[f] = true
			unique = append(unique, f)
		}
	}
	return unique
}

func generateInput(feature, testType string) string {
	switch testType {
	case "normal":
		return fmt.Sprintf("valid input for %s", feature)
	case "edge":
		return "empty string, zero value, maximum length"
	case "negative":
		return "invalid input, malformed data, missing fields"
	case "boundary":
		return "just below/above threshold, empty array, single element"
	default:
		return "generic input"
	}
}

func generateExpected(feature, testType string) string {
	switch testType {
	case "normal":
		return "successful result, expected output"
	case "edge":
		return "graceful handling of edge case, default values"
	case "negative":
		return "error returned, rejection, proper error message"
	case "boundary":
		return "correct behavior at boundary, no crash"
	default:
		return "expected behavior"
	}
}

func writeEnhancedGoTests(suite TestSuite, dir string) {
	file := filepath.Join(dir, "generated_test.go")
	var sb strings.Builder
	sb.WriteString("package tests\n\nimport \"testing\"\n\n")

	for _, tc := range suite.TestCases {
		sb.WriteString(fmt.Sprintf(`func Test_%s(t *testing.T) {
	// %s
	// Type: %s, Priority: %s
	// Input: %s
	// Expected: %s
	t.Skip("TODO: implement test")
}

`, tc.Name, tc.Description, tc.Type, tc.Priority, tc.Input, tc.Expected))
	}
	os.WriteFile(file, []byte(sb.String()), 0644)
}

func writeEnhancedNodeTests(suite TestSuite, dir string) {
	file := filepath.Join(dir, "generated.test.js")
	var sb strings.Builder
	sb.WriteString("// Auto-generated test cases\n\n")
	for _, tc := range suite.TestCases {
		sb.WriteString(fmt.Sprintf(`describe('%s', () => {
	it('should handle %s', () => {
		// Input: %s
		// Expected: %s
		expect(true).toBe(true); // TODO: implement
	});
});

`, tc.Name, tc.Type, tc.Input, tc.Expected))
	}
	os.WriteFile(file, []byte(sb.String()), 0644)
}

func writeEnhancedPythonTests(suite TestSuite, dir string) {
	file := filepath.Join(dir, "test_generated.py")
	var sb strings.Builder
	sb.WriteString("# Auto-generated test cases\nimport pytest\n\n")
	for _, tc := range suite.TestCases {
		sb.WriteString(fmt.Sprintf(`def test_%s():
	"""%s - Input: %s, Expected: %s"""
	pass  # TODO: implement

`, tc.Name, tc.Description, tc.Input, tc.Expected))
	}
	os.WriteFile(file, []byte(sb.String()), 0644)
}

func sanitizeName(s string) string {
	reg := strings.NewReplacer(
		" ", "_", "-", "_", ".", "_", "/", "_", "\\", "_",
		":", "_", ";", "_", ",", "_", "(", "", ")", "",
		"[", "", "]", "", "{", "", "}", "",
		"\"", "", "'", "", "?", "", "!", "",
		"@", "", "#", "", "$", "", "%", "", "^", "",
		"&", "", "*", "", "=", "", "+", "",
		"<", "", ">", "", "|", "", "~", "", "`", "",
	)
	result := reg.Replace(s)
	if len(result) > 50 {
		result = result[:50]
	}
	return strings.ToLower(result)
}