package main

import (
	"fmt"
	"os"
	"strings"
	"testing"
)

// These integration tests exercise run* entry points that invoke real Go tooling
// (go test / go vet) inside a throwaway Go module. Slower but covers the
// external-command paths that pure unit tests can't reach.

func TestRunCodeQualityGateGoProject(t *testing.T) {
	dir := chdirTemp(t)
	writeRelFile(t, dir, "go.mod", "module demo\n\ngo 1.21\n")
	writeRelFile(t, dir, "src/main.go", `package main

func main() {}

func add(a, b int) int { return a + b }
`)
	writeRelFile(t, dir, "src/main_test.go", `package main

import "testing"

func TestAdd(t *testing.T) {
	if add(1, 2) != 3 {
		t.Fail()
	}
}
`)

	code := runWithExitCapture(t, func() { runCodeQualityGate(nil) })
	if code != 0 {
		t.Errorf("quality gate on clean Go project should pass, got exit %d", code)
	}
}

func TestRunCodeQualityGateStrictFailsOnLowCoverage(t *testing.T) {
	dir := chdirTemp(t)
	writeRelFile(t, dir, "go.mod", "module demo\n\ngo 1.21\n")
	// A lot of uncovered code + a single tiny test → coverage in (0, 80)%.
	var sb strings.Builder
	sb.WriteString("package main\n\nfunc main() {}\n")
	for i := 0; i < 30; i++ {
		sb.WriteString(fmt.Sprintf("func fn%d(x int) int { return x * %d }\n", i, i+1))
	}
	writeRelFile(t, dir, "src/main.go", sb.String())
	writeRelFile(t, dir, "src/main_test.go", `package main

import "testing"

func TestFn0(t *testing.T) {
	if fn0(2) != 0 {
		t.Fail()
	}
}
`)

	code := runWithExitCapture(t, func() { runCodeQualityGate([]string{"--strict"}) })
	if code != 1 {
		t.Errorf("strict gate with low coverage should fail, got exit %d", code)
	}
}

func TestRunAssessMaturity(t *testing.T) {
	dir := chdirTemp(t)
	writeRelFile(t, dir, "go.mod", "module demo\n\ngo 1.21\n")
	for i := 0; i < 3; i++ {
		writeRelFile(t, dir, fmt.Sprintf("src/mod%d.go", i), "package main\n\nfunc main() {}\n")
		writeRelFile(t, dir, fmt.Sprintf("src/mod%d_test.go", i), "package main\n\nimport \"testing\"\n\nfunc TestX(t *testing.T) {}\n")
	}
	writeRelFile(t, dir, "docs/architecture/ARCHITECTURE.md", "# arch")
	writeRelFile(t, dir, "docs/architecture/modules/auth.md", "# auth module")
	writeRelFile(t, dir, "docs/tasks/TASKS.md", "| T-001 | x | P0 | ✅ | |")
	writeRelFile(t, dir, "docs/tasks/SESSION-STATE.md", "# s")

	// runAssessMaturity prints (no exit on success); exercise the entry + report.
	runWithExitCapture(t, func() { runAssessMaturity(nil) })
	report := assessProjectMaturity()
	if report.Score <= 0 {
		t.Errorf("maturity score should be > 0 for a populated project, got %d", report.Score)
	}
	if report.Level == 0 {
		t.Error("maturity level should be assigned")
	}
}

func TestRunAssessMaturityEmptyProject(t *testing.T) {
	chdirTemp(t)
	// Empty dir → level 1, still exits 0.
	runWithExitCapture(t, func() { runAssessMaturity(nil) })
	if report := assessProjectMaturity(); report.Level != 1 {
		t.Errorf("empty project should be level 1, got %d", report.Level)
	}
}

func TestRunMutationTestGoProject(t *testing.T) {
	dir := chdirTemp(t)
	writeRelFile(t, dir, "go.mod", "module demo\n\ngo 1.21\n")
	writeRelFile(t, dir, "src/main.go", `package main

func add(a, b int) int { return a + b }
func mul(a, b int) int { return a * b }
func sub(a, b int) int { return a - b }
func main() {}
`)
	writeRelFile(t, dir, "src/main_test.go", `package main

import "testing"

func TestArithmetic(t *testing.T) {
	if add(1, 2) != 3 || add(-1, 1) != 0 {
		t.Fail()
	}
	if mul(2, 3) != 6 || mul(0, 5) != 0 {
		t.Fail()
	}
	if sub(5, 2) != 3 || sub(0, 5) != -5 {
		t.Fail()
	}
}
`)

	orig, err := os.ReadFile("src/main.go")
	if err != nil {
		t.Fatal(err)
	}

	runWithExitCapture(t, func() {
		runMutationTest([]string{"--mutants", "5"})
	})

	after, err := os.ReadFile("src/main.go")
	if err != nil {
		t.Fatal(err)
	}
	if string(orig) != string(after) {
		t.Error("mutation test should revert source file modifications after running")
	}
}
