// post-commit-verify: PreToolUse Hook on git commit
// Ensures a commit that references a task (T-XXX) passes tests before landing.
// If the commit message contains a WU-{Sprint}-{NN} or T-XXX marker, runs a fast
// verification (tests for the changed packages only). On failure, blocks the commit.
//
// Exit 0 = allow, exit 2 = block (tool error).
package main

import (
	"fmt"
	"os"
	"os/exec"
	"regexp"
)

func runPostCommitVerify(args []string) {
	input, err := readHookInput()
	if err != nil {
		osExit(0)
	}
	if input.ToolName != "Bash" {
		osExit(0)
	}
	command := input.Input["command"]
	if !isGitCommit(command) {
		osExit(0)
	}

	// Only enforce when a task or WU marker is present in the commit message.
	taskMarker := regexp.MustCompile(`(T-\d{3}|WU-\d+-\d+)`)
	if !taskMarker.MatchString(command) {
		// No task marker: still allow, but advise.
		fmt.Fprintf(os.Stderr, "\n[verify-commit] HINT: commit 未包含 T-XXX 或 WU-{Sprint}-{NN} 标记。\n")
		fmt.Fprintf(os.Stderr, "[verify-commit] 建议 commit message 形如: 'feat(ui): add settings tab (WU-2-03)' 或 'fix: ... (T-108)'\n")
		osExit(0)
	}

	// Detect project type and run a fast changed-only test.
	projectType := detectProjectType()
	if projectType == "unknown" {
		osExit(0)
	}

	fmt.Fprintf(os.Stderr, "\n[verify-commit] 检测到任务标记，运行前置验收（%s）...\n", projectType)
	changedFiles := getChangedFiles()
	if len(changedFiles) == 0 {
		// Nothing new to test — allow.
		osExit(0)
	}

	var cmd *exec.Cmd
	switch projectType {
	case "go":
		packages := getChangedPackages(changedFiles, ".go")
		if len(packages) == 0 {
			fmt.Fprintf(os.Stderr, "[verify-commit] 无 Go 文件变更，跳过测试\n")
			osExit(0)
		}
		cmd = exec.Command("go", append([]string{"test", "-p", "1"}, packages...)...)
	case "node":
		cmd = exec.Command("npm", "test")
	case "python":
		pyFiles := filterByExt(changedFiles, ".py")
		if len(pyFiles) == 0 {
			osExit(0)
		}
		cmd = exec.Command("pytest", pyFiles...)
	default:
		osExit(0)
	}

	cmd.Stdout = os.Stderr
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "\n[verify-commit] ❌ 前置测试失败，阻止提交。\n")
		fmt.Fprintf(os.Stderr, "[verify-commit] 请先修复测试或运行 'ai-guard verify-task T-XXX' 完成验收。\n")
		osExit(2)
	}

	fmt.Fprintf(os.Stderr, "[verify-commit] ✅ 前置测试通过。\n")
	osExit(0)
}
