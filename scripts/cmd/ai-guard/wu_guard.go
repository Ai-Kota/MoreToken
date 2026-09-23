// wu-guard: PreToolUse Hook
// Enforces WU (Work Unit) granularity on git commits.
// Blocks any commit that stages > maxFilesPerCommit files without a WU-{Sprint}-{NN} marker.
// Exit 0 = allow, exit 2 = block (tool error).
package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
)

const (
	maxFilesPerCommit = 5
)

// runWuGuard is invoked by main.go.
func runWuGuard(args []string) {
	input, err := readHookInput()
	if err != nil {
		// Not running as a hook (manual invocation) — nothing to enforce.
		osExit(0)
	}

	// Only enforce on Bash commands that are git commit.
	if input.ToolName != "Bash" {
		osExit(0)
	}
	command := input.Input["command"]
	if !isGitCommit(command) {
		osExit(0)
	}

	// Find git repo root.
	repoDir := findGitDir()
	if repoDir == "" {
		osExit(0)
	}

	// Check staged file count.
	stagedFiles := getStagedFiles(repoDir)
	if len(stagedFiles) == 0 {
		osExit(0)
	}

	if len(stagedFiles) > maxFilesPerCommit {
		if !hasWUFlag(command) {
			fmt.Fprintf(os.Stderr, "\n[wu-guard] REJECTED: %d files staged (max %d)\n", len(stagedFiles), maxFilesPerCommit)
			fmt.Fprintf(os.Stderr, "[wu-guard] Files:\n")
			for _, f := range stagedFiles {
				fmt.Fprintf(os.Stderr, "  - %s\n", f)
			}
			fmt.Fprintf(os.Stderr, "[wu-guard] 模块分布（关注点聚类）:\n")
			for mod, cnt := range countModules(stagedFiles) {
				fmt.Fprintf(os.Stderr, "  %s: %d 文件\n", mod, cnt)
			}
			fmt.Fprintf(os.Stderr, "\n[wu-guard] HINT: Split into WUs (Work Units) — one WU = one commit ≤ %d files.\n", maxFilesPerCommit)
			fmt.Fprintf(os.Stderr, "[wu-guard] HINT: Add WU-{Sprint}-{NN} to commit message, e.g. 'feat(ui): settings tab (WU-2-03)'\n")
			fmt.Fprintf(os.Stderr, "[wu-guard] HINT: To force through (reviewable large refactor): append 'BYPASS-WU-SCOPE' to message.\n")
			osExit(2) // exit 2 = tool error → blocks the command
		}
	}

	// Advisory for medium commits.
	if len(stagedFiles) > 3 {
		fmt.Fprintf(os.Stderr, "\n[wu-guard] WARNING: %d files staged (recommended ≤ %d)\n", len(stagedFiles), maxFilesPerCommit)
		fmt.Fprintf(os.Stderr, "[wu-guard] Consider splitting into smaller WUs for reviewability.\n")
	}

	osExit(0)
}

// isGitCommit reports whether a bash command is a git commit invocation.
func isGitCommit(command string) bool {
	c := strings.TrimSpace(command)
	if !strings.HasPrefix(c, "git") {
		return false
	}
	// Strip leading git.
	rest := strings.TrimSpace(strings.TrimPrefix(c, "git"))
	return strings.HasPrefix(rest, "commit") || strings.HasPrefix(rest, "-c") && strings.Contains(c, "commit")
}

// findGitDir walks up from cwd looking for a .git directory.
func findGitDir() string {
	dir, err := os.Getwd()
	if err != nil {
		return ""
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, ".git")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return ""
		}
		dir = parent
	}
}

// getStagedFiles returns the list of staged file paths.
func getStagedFiles(repoDir string) []string {
	cmd := exec.Command("git", "diff", "--cached", "--name-only")
	cmd.Dir = repoDir
	output, err := cmd.Output()
	if err != nil {
		return nil
	}
	var files []string
	for _, line := range strings.Split(string(output), "\n") {
		line = strings.TrimSpace(line)
		if line != "" {
			files = append(files, line)
		}
	}
	return files
}

// hasWUFlag reports whether the command message contains a WU marker or bypass token.
func hasWUFlag(command string) bool {
	wuPattern := regexp.MustCompile(`(?i)WU-\d+-\d+`)
	bypassPattern := regexp.MustCompile(`(?i)BYPASS-WU-SCOPE`)
	return wuPattern.MatchString(command) || bypassPattern.MatchString(command)
}

// countModules clusters staged files by their top-level module so the rejection
// message can show how many distinct concerns a commit spans. A commit touching
// 3 modules is far less atomic than one touching 3 files in the same module.
func countModules(files []string) map[string]int {
	mods := map[string]int{}
	for _, f := range files {
		parts := strings.Split(f, "/")
		var key string
		switch {
		case len(parts) >= 3:
			key = parts[0] + "/" + parts[1] // nested: src/nested/...
		case len(parts) == 2:
			key = parts[0] // top-level file under a dir: src/a.go
		default:
			key = "root" // bare file at repo root: go.mod
		}
		mods[key]++
	}
	return mods
}
