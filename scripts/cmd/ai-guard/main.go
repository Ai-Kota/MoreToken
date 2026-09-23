// ai-guard: Unified CLI for all AI coding constraint tools
// Single binary with subcommands for all 13 tools.
// Usage: ai-guard <command> [args...]
//
// Commands:
//
//	env-protect               System environment protection
//	project-assess            Project risk/cost assessment
//	code-quality-gate         Code quality checks
//	test-designer             Test case generation
//	mutation-test             Mutation testing
//	check-read-before-write   Read-before-write hook
//	check-design-doc          Design document hook
//	check-scope               Scope check hook
//	track-read                Track file reads
//	clear-read                Clear read tracking
//	pre-task-check            Task pre-check
//	verify-task               Task verification
//	constraint-metrics        Constraint effectiveness metrics
//	checkpoint                Save progress checkpoint
//	assess-maturity           Evaluate project maturity level
//	generate-settings         Generate settings.json by maturity level
//	build                     Rebuild ai-guard binary
//	version                   Show version
package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
)

// Version is a var (not const) so build.sh can inject a git-derived version
// via -ldflags "-X main.Version=..."; fallback is the release constant.
var Version = "3.6.0"

// osExit is indirection over os.Exit so subcommand entry points (run*) can be
// unit-tested: tests swap it for a function that records the code and panics,
// then recover. Runtime behavior is unchanged (defaults to os.Exit).
var osExit = os.Exit

func main() {
	if len(os.Args) < 2 {
		printUsage()
		osExit(0)
	}

	cmd := os.Args[1]
	args := os.Args[2:]

	switch cmd {
	case "version", "-v", "--version":
		fmt.Printf("ai-guard v%s (%s/%s)\n", Version, runtime.GOOS, runtime.GOARCH)

	case "help", "-h", "--help":
		printUsage()

	case "build":
		buildSelf()

	// 环境保护
	case "env-protect":
		runEnvProtect(args)

	// 项目评估
	case "project-assess":
		runProjectAssess(args)

	// 质量门禁
	case "code-quality-gate":
		runCodeQualityGate(args)
	case "test-designer":
		runTestDesigner(args)
	case "mutation-test":
		runMutationTest(args)

	// Hook 工具
	case "check-read-before-write":
		runCheckReadBeforeWrite(args)
	case "check-design-doc":
		runCheckDesignDoc(args)
	case "check-scope":
		runCheckScope(args)
	case "check-bash-write":
		runCheckBashWrite(args)
	case "track-read":
		runTrackRead(args)
	case "clear-read":
		runClearRead(args)
	case "wu-guard":
		runWuGuard(args)
	case "session-bootstrap":
		runSessionBootstrap(args)
	case "verify-commit":
		runPostCommitVerify(args)

	// 门禁工具
	case "pre-task-check":
		runPreTaskCheck(args)
	case "begin-task":
		runBeginTask(args)
	case "end-task":
		runEndTask(args)
	case "verify-task":
		runVerifyTask(args)
	case "constraint-metrics":
		runConstraintMetrics(args)
	case "checkpoint":
		runCheckpoint(args)
	case "assess-maturity":
		runAssessMaturity(args)
	case "generate-settings":
		runGenerateSettings(args)

	default:
		fmt.Fprintf(os.Stderr, "Unknown command: %s\n", cmd)
		fmt.Fprintf(os.Stderr, "Run 'ai-guard help' for usage\n")
		osExit(1)
	}
}

func printUsage() {
	fmt.Println("ai-guard - AI Coding Constraint Tools")
	fmt.Println()
	fmt.Printf("Version: %s (%s/%s)\n", Version, runtime.GOOS, runtime.GOARCH)
	fmt.Println()
	fmt.Println("Usage: ai-guard <command> [args...]")
	fmt.Println()
	fmt.Println("Commands:")
	fmt.Println("  环境保护")
	fmt.Println("    env-protect              System environment protection hook")
	fmt.Println()
	fmt.Println("  项目评估")
	fmt.Println("    project-assess <desc>    Project risk/cost assessment")
	fmt.Println()
	fmt.Println("  质量门禁")
	fmt.Println("    code-quality-gate        Code quality checks")
	fmt.Println("    test-designer <req>      Test case generation")
	fmt.Println("    mutation-test            Mutation testing")
	fmt.Println()
	fmt.Println("  Hook 工具")
	fmt.Println("    check-read-before-write  Read-before-write hook")
	fmt.Println("    check-design-doc         Design document hook")
	fmt.Println("    check-scope              Scope check hook")
	fmt.Println("    track-read               Track file reads")
	fmt.Println("    clear-read               Clear read tracking")
	fmt.Println("    wu-guard                 WU commit size gate (pre-commit hook)")
	fmt.Println("    verify-commit            Pre-commit task verification hook")
	fmt.Println()
	fmt.Println("  门禁工具")
	fmt.Println("    pre-task-check <id>      Task pre-check")
	fmt.Println("    begin-task <id>          Declare active task (intent gate)")
	fmt.Println("    end-task                  Clear active task context")
	fmt.Println("    verify-task <id>         Task verification")
	fmt.Println("    constraint-metrics       Constraint metrics")
	fmt.Println()
	fmt.Println("  进度管理")
	fmt.Println("    checkpoint [task-id]     Save progress to SESSION-STATE.md")
	fmt.Println("    session-bootstrap        SessionStart context snapshot hook")
	fmt.Println()
	fmt.Println("  项目评估")
	fmt.Println("    assess-maturity          Evaluate project maturity (1-3)")
	fmt.Println("    generate-settings <lvl>  Generate settings.json (1/2/3)")
	fmt.Println()
	fmt.Println("  其他")
	fmt.Println("    build                    Rebuild ai-guard binary")
	fmt.Println("    version                  Show version")
	fmt.Println("    help                     Show this help")
}

// buildSelf recompiles ai-guard from source.
func buildSelf() {
	fmt.Println("Rebuilding ai-guard...")

	// Find the source directory
	exe, err := os.Executable()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error getting executable path: %v\n", err)
		osExit(1)
	}

	// Try to find go.mod by walking up from the executable's directory.
	// stopAt="" → walk to root (rebuild is invoked from a checked-out tree).
	exeDir := filepath.Dir(exe)
	srcDir := findSrcDir(exeDir, "")
	if srcDir == "" {
		// Fallback: assume we're running from the scripts directory
		srcDir = "."
	}

	output := filepath.Join(exeDir, "ai-guard")
	if runtime.GOOS == "windows" {
		output += ".exe"
	}

	cmd := exec.Command("go", "build", "-o", output, "./cmd/ai-guard")
	cmd.Dir = srcDir
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	if err := cmd.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "Build failed: %v\n", err)
		osExit(1)
	}

	fmt.Printf("✅ ai-guard built to %s\n", output)
}

// findSrcDir walks up from dir looking for go.mod, stopping at stopAt if
// reached first. stopAt bounds the upward search so callers that want "within
// this tree only" can say so — a stray go.mod in an unrelated ancestor
// (e.g. C:\Users\…\Temp\go.mod on this machine) is never returned.
// Pass "" to allow walking to the filesystem root (original behavior).
func findSrcDir(dir, stopAt string) string {
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		if stopAt != "" && dir == stopAt {
			return ""
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return ""
		}
		dir = parent
	}
}
