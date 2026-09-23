// mutation-test: Mutation testing for code quality verification
// Intentionally introduces bugs (mutants) and checks if tests catch them.
// Higher mutation score = better test quality.
// Usage: mutation-test [--mutants <count>] [--verbose] [--parallel]
package main

import (
	"crypto/md5"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

type Mutant struct {
	ID         string `json:"id"`
	File       string `json:"file"`
	Line       int    `json:"line"`
	Original   string `json:"original"`
	Mutated    string `json:"mutated"`
	Type       string `json:"type"`
	Killed     bool   `json:"killed"`
	TestOutput string `json:"test_output,omitempty"`
}

type MutationReport struct {
	TotalMutants    int      `json:"total_mutants"`
	KilledMutants   int      `json:"killed_mutants"`
	SurvivedMutants int      `json:"survived_mutants"`
	MutationScore   float64  `json:"mutation_score"`
	Mutants         []Mutant `json:"mutants"`
	Duration        string   `json:"duration"`
	Passed          bool     `json:"passed"`
	ProjectType     string   `json:"project_type"`
}

// mutationOperators defines mutation operators for various languages.
// Each operator has a name, a regex pattern to match, and a replacement.
// The pattern is applied to the line content.
var mutationOperators = []struct {
	Name        string
	Pattern     string
	Replacement string
	Lang        string // "go", "js", "py", "rust", "all"
}{
	// Arithmetic operators (all languages)
	{"AOR1", `\+`, `-`, "all"},
	{"AOR2", `\-`, `+`, "all"},
	{"AOR3", `\*`, `/`, "all"},
	{"AOR4", `\/`, `*`, "all"},
	{"AOR5", `%`, `*`, "all"},

	// Relational operators (all languages)
	{"ROR1", `==`, `!=`, "all"},
	{"ROR2", `!=`, `==`, "all"},
	{"ROR3", `>`, `<=`, "all"},
	{"ROR4", `<`, `>=`, "all"},
	{"ROR5", `>=`, `<`, "all"},
	{"ROR6", `<=`, `>`, "all"},

	// Logical operators (all languages)
	{"LOR1", `&&`, `||`, "all"},
	{"LOR2", `||`, `&&`, "all"},

	// Conditional operators (all languages)
	{"CRP1", `> `, `>= `, "all"},
	{"CRP2", `< `, `<= `, "all"},

	// Increment/decrement (all languages)
	{"CVR1", `\+\+`, `--`, "all"},
	{"CVR2", `--`, `++`, "all"},

	// Return value mutations (language specific)
	{"RRV1_GO", `return true`, `return false`, "go"},
	{"RRV2_GO", `return false`, `return true`, "go"},
	{"RRV3_GO", `return nil`, `return fmt.Errorf("mutant")`, "go"},
	{"RRV4_GO", `return 0`, `return 1`, "go"},
	{"RRV5_GO", `return 1`, `return 0`, "go"},
	{"RRV6_GO", `return ""`, `return "mutant"`, "go"},
	{"RRV7_GO", `return err`, `return nil`, "go"}, // Careful: this could cause nil deref

	{"RRV1_JS", `return true`, `return false`, "js"},
	{"RRV2_JS", `return false`, `return true`, "js"},
	{"RRV3_JS", `return null`, `return undefined`, "js"},
	{"RRV4_JS", `return 0`, `return 1`, "js"},
	{"RRV5_JS", `return ""`, `return "mutant"`, "js"},

	{"RRV1_PY", `return True`, `return False`, "py"},
	{"RRV2_PY", `return False`, `return True`, "py"},
	{"RRV3_PY", `return None`, `return "mutant"`, "py"},
	{"RRV4_PY", `return 0`, `return 1`, "py"},
	{"RRV5_PY", `return ""`, `return "mutant"`, "py"},

	{"RRV1_RS", `return true`, `return false`, "rust"},
	{"RRV2_RS", `return false`, `return true`, "rust"},
	{"RRV3_RS", `return Ok`, `return Err`, "rust"},
	{"RRV4_RS", `return Some`, `return None`, "rust"},
	{"RRV5_RS", `return 0`, `return 1`, "rust"},
	{"RRV6_RS", `return ""`, `return "mutant"`, "rust"},
}

var verboseMode = false
var parallelMode = false

func runMutationTest(args []string) {
	maxMutants := 10
	verbose := false
	parallel := false

	for i, arg := range args {
		if arg == "--mutants" && i+1 < len(args) {
			if val, err := strconv.Atoi(args[i+1]); err == nil {
				maxMutants = val
			}
		}
		if arg == "--verbose" {
			verbose = true
			verboseMode = true
		}
		if arg == "--parallel" {
			parallel = true
			parallelMode = true
		}
	}

	fmt.Println("==================================================")
	fmt.Println("🧬 变异测试")
	fmt.Println("==================================================")
	fmt.Println()

	start := time.Now()

	projectType := detectProjectType()
	fmt.Printf("项目类型: %s\n", projectType)
	if projectType == "unknown" {
		fmt.Println("⚠️  无法检测项目类型，跳过变异测试")
		osExit(0)
	}

	fmt.Println("\n1️⃣ 运行现有测试...")
	if !runExistingTestsForMutation(projectType) {
		fmt.Println("❌ 现有测试未通过，无法进行变异测试")
		osExit(1)
	}
	fmt.Println("   ✅ 现有测试通过")

	fmt.Println("\n2️⃣ 查找源代码文件...")
	sourceFiles := findSourceFiles(projectType)
	fmt.Printf("   找到 %d 个源文件\n", len(sourceFiles))

	if len(sourceFiles) == 0 {
		fmt.Println("⚠️  未找到源文件，跳过变异测试")
		osExit(0)
	}

	fmt.Println("\n3️⃣ 生成变异体...")
	mutants := generateMutants(sourceFiles, maxMutants, projectType)
	fmt.Printf("   生成了 %d 个变异体\n", len(mutants))

	if len(mutants) == 0 {
		fmt.Println("⚠️  没有生成有效的变异体，跳过测试")
		osExit(0)
	}

	fmt.Println("\n4️⃣ 测试变异体...")
	var wg sync.WaitGroup
	var mu sync.Mutex
	killed := 0
	survived := 0

	// If parallel, use goroutines with limited concurrency
	maxConcurrent := 4
	if parallel {
		sem := make(chan struct{}, maxConcurrent)
		for i := range mutants {
			wg.Add(1)
			go func(idx int) {
				defer wg.Done()
				sem <- struct{}{}
				defer func() { <-sem }()
				testMutant(&mutants[idx], projectType, &killed, &survived, &mu)
			}(i)
		}
		wg.Wait()
	} else {
		for i := range mutants {
			testMutant(&mutants[i], projectType, &killed, &survived, &mu)
		}
	}

	duration := time.Since(start)
	score := 0.0
	if len(mutants) > 0 {
		score = float64(killed) / float64(len(mutants)) * 100
	}

	report := MutationReport{
		TotalMutants:    len(mutants),
		KilledMutants:   killed,
		SurvivedMutants: survived,
		MutationScore:   score,
		Mutants:         mutants,
		Duration:        duration.Round(time.Millisecond).String(),
		Passed:          score >= 70.0,
		ProjectType:     projectType,
	}

	fmt.Println("\n5️⃣ 生成报告...")
	printMutationReport(report, verbose)
	saveMutationReport(report)

	if report.Passed {
		fmt.Println("\n✅ 变异测试通过")
		osExit(0)
	} else {
		fmt.Println("\n❌ 变异测试失败（变异分数 < 70%）")
		fmt.Println("   建议：增加测试用例以覆盖更多边界情况")
		osExit(1)
	}
}

// mutantFileLocks serializes mutants that touch the same file. Concurrent
// apply/revert on the same file corrupt each other's content, so each file gets
// its own mutex: different files still run in parallel.
var mutantFileLocks sync.Map

func testMutant(mutant *Mutant, projectType string, killedPtr, survivedPtr *int, mu *sync.Mutex) {
	// Serialize mutants on the same file.
	lockI, _ := mutantFileLocks.LoadOrStore(mutant.File, &sync.Mutex{})
	lock := lockI.(*sync.Mutex)
	lock.Lock()
	defer lock.Unlock()

	// Snapshot the whole file so the restore is exact. Line-based revert
	// corrupts the source when multiple mutants overlap the same file (or when
	// a parallel mutant on the same file shifts line numbers).
	orig, err := os.ReadFile(mutant.File)
	if err != nil {
		return
	}

	applyMutation(mutant)
	testPassed := runExistingTestsForMutation(projectType)
	mutant.Killed = !testPassed

	// Update counts safely
	mu.Lock()
	if mutant.Killed {
		(*killedPtr)++
	} else {
		(*survivedPtr)++
	}
	mu.Unlock()

	if verboseMode || !mutant.Killed {
		status := "✅"
		if !mutant.Killed {
			status = "⚠️"
		}
		fmt.Printf("   %s 变异体 %s (%s) - %s\n", status, mutant.ID, mutant.Type, map[bool]string{true: "已捕获", false: "逃逸"}[mutant.Killed])
	}

	// Restore the full file snapshot.
	os.WriteFile(mutant.File, orig, 0644)
}

func runExistingTestsForMutation(projectType string) bool {
	var cmd *exec.Cmd
	switch projectType {
	case "go":
		cmd = exec.Command("go", "test", "-p", "1", "./...")
	case "node":
		cmd = exec.Command("npm", "test")
	case "python":
		cmd = exec.Command("pytest")
	case "rust":
		cmd = exec.Command("cargo", "test")
	default:
		return true
	}
	cmd.Stdout = nil // discard output for speed
	cmd.Stderr = nil
	err := cmd.Run()
	return err == nil
}

func findSourceFiles(projectType string) []string {
	var extensions []string
	switch projectType {
	case "go":
		extensions = []string{".go"}
	case "node":
		extensions = []string{".ts", ".js", ".tsx", ".jsx"}
	case "python":
		extensions = []string{".py"}
	case "rust":
		extensions = []string{".rs"}
	default:
		return nil
	}
	// Recursively scan src/ plus the whole project root. filepath.Glob's "**"
	// does not recurse in Go, so a manual WalkDir is required to find files in
	// nested directories (e.g. cmd/ai-guard/*.go).
	all := findFilesByExt("src", extensions)
	all = append(all, findFilesByExt(".", extensions)...)

	// Dedupe (src/ is also under ".") and exclude test files — mutating tests
	// would only probe the tests themselves, not the production source.
	seen := make(map[string]bool)
	var files []string
	for _, f := range all {
		if seen[f] {
			continue
		}
		seen[f] = true
		if strings.HasSuffix(f, "_test.go") || strings.HasSuffix(f, "_test.py") {
			continue
		}
		files = append(files, f)
	}
	return files
}

func generateMutants(files []string, maxMutants int, projectType string) []Mutant {
	var mutants []Mutant

	// Map project type to language shorthand for operator filtering
	var langKey string
	switch projectType {
	case "go":
		langKey = "go"
	case "node":
		langKey = "js"
	case "python":
		langKey = "py"
	case "rust":
		langKey = "rust"
	default:
		langKey = "all"
	}

	for _, file := range files {
		if len(mutants) >= maxMutants {
			break
		}
		data, err := os.ReadFile(file)
		if err != nil {
			continue
		}
		content := string(data)
		lines := strings.Split(content, "\n")

		for lineNum, line := range lines {
			if len(mutants) >= maxMutants {
				break
			}
			trimmed := strings.TrimSpace(line)
			if trimmed == "" || strings.HasPrefix(trimmed, "//") || strings.HasPrefix(trimmed, "#") || strings.HasPrefix(trimmed, "/*") {
				continue
			}

			// Try each operator
			for _, op := range mutationOperators {
				if len(mutants) >= maxMutants {
					break
				}
				// Filter by language
				if op.Lang != "all" && op.Lang != langKey {
					continue
				}
				re := regexp.MustCompile(op.Pattern)
				if re.MatchString(line) {
					mutated := re.ReplaceAllString(line, op.Replacement)
					if mutated != line && !strings.Contains(mutated, "mutant") { // avoid self-referential loops
						id := generateMutantID(file, lineNum, op.Name)
						mutants = append(mutants, Mutant{
							ID:       id,
							File:     file,
							Line:     lineNum + 1,
							Original: line,
							Mutated:  mutated,
							Type:     op.Name,
						})
						break
					}
				}
			}
		}
	}
	return mutants
}

func generateMutantID(file string, line int, op string) string {
	data := fmt.Sprintf("%s:%d:%s", file, line, op)
	hash := md5.Sum([]byte(data))
	return fmt.Sprintf("M-%x", hash[:4])
}

func applyMutation(mutant *Mutant) {
	data, err := os.ReadFile(mutant.File)
	if err != nil {
		return
	}
	content := string(data)
	lines := strings.Split(content, "\n")
	if mutant.Line-1 < len(lines) {
		lines[mutant.Line-1] = mutant.Mutated
	}
	os.WriteFile(mutant.File, []byte(strings.Join(lines, "\n")), 0644)
}

func revertMutation(mutant *Mutant) {
	data, err := os.ReadFile(mutant.File)
	if err != nil {
		return
	}
	content := string(data)
	lines := strings.Split(content, "\n")
	if mutant.Line-1 < len(lines) {
		lines[mutant.Line-1] = mutant.Original
	}
	os.WriteFile(mutant.File, []byte(strings.Join(lines, "\n")), 0644)
}

func printMutationReport(report MutationReport, verbose bool) {
	fmt.Println("==================================================")
	fmt.Println("📊 变异测试报告")
	fmt.Println("==================================================")
	fmt.Println()
	fmt.Printf("项目类型: %s\n", report.ProjectType)
	fmt.Printf("变异体总数: %d\n", report.TotalMutants)
	fmt.Printf("已捕获: %d\n", report.KilledMutants)
	fmt.Printf("逃逸: %d\n", report.SurvivedMutants)
	fmt.Printf("变异分数: %.1f%%\n", report.MutationScore)
	fmt.Printf("耗时: %s\n", report.Duration)
	fmt.Println()

	scoreBar := fmt.Sprintf("[ %s %s ]",
		strings.Repeat("█", int(report.MutationScore/5)),
		strings.Repeat("░", 20-int(report.MutationScore/5)))
	fmt.Printf("分数: %s %.1f%%\n", scoreBar, report.MutationScore)
	fmt.Println()

	if report.SurvivedMutants > 0 && verbose {
		fmt.Println("⚠️  逃逸的变异体:")
		for _, m := range report.Mutants {
			if !m.Killed {
				fmt.Printf("  %s: %s:%d (%s)\n", m.ID, m.File, m.Line, m.Type)
				fmt.Printf("    原始: %s\n", m.Original)
				fmt.Printf("    变异: %s\n", m.Mutated)
			}
		}
		fmt.Println()
	} else if report.SurvivedMutants > 0 {
		fmt.Printf("（使用 --verbose 查看逃逸变异体详情）\n\n")
	}

	fmt.Println("📋 阈值:")
	if report.MutationScore >= 70 {
		fmt.Println("   ✅ 变异分数 ≥ 70%（通过）")
	} else {
		fmt.Println("   ❌ 变异分数 < 70%（失败）")
	}
}

func saveMutationReport(report MutationReport) {
	os.MkdirAll(".claude/metrics", 0755)
	data, _ := json.MarshalIndent(report, "", "  ")
	os.WriteFile(".claude/metrics/mutation-report.json", data, 0644)
	fmt.Println("   📁 报告已保存到 .claude/metrics/mutation-report.json")
}