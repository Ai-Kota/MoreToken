// check-read-before-write: PreToolUse Hook
// Validates that a file was read before writing to it.
// Exit 0 = allow, exit 1 = block.
package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"spt/scripts/internal/tracker"
)

func runCheckReadBeforeWrite(args []string) {
	input, err := readHookInput()
	if err != nil {
		// On error, allow the operation
		osExit(0)
	}

	filePath := extractFilePath(input)
	if filePath == "" {
		osExit(0)
	}

	// Convert absolute paths to relative paths to match tracker storage
	// and prevent Windows drive letters from causing issues.
	if filepath.IsAbs(filePath) {
		cwd, err := os.Getwd()
		if err == nil {
			if rel, err := filepath.Rel(cwd, filePath); err == nil {
				filePath = rel
			}
		}
	}

	filePath = normalizePath(filePath)

	// Skip tracker files themselves
	if strings.HasPrefix(filePath, ".claude/") {
		osExit(0)
	}

	// If file was read, allow
	if tracker.IsRead(filePath) {
		osExit(0)
	}

	// If file doesn't exist, check task boundary allowance
	if !fileExists(filePath) {
		if isTaskBoundaryAllowed(filePath) {
			osExit(0)
		}
		// Allow new files in src/, tests/, docs/ by default
		for _, prefix := range []string{"src/", "tests/", "docs/"} {
			if strings.HasPrefix(filePath, prefix) {
				osExit(0)
			}
		}
	}

	// File exists but wasn't read: block
	fmt.Fprintf(os.Stderr, "ERROR: File %s not read before write.\n"+
		"       Please run: Read %s\n", filePath, filePath)
	osExit(1)
}

func isTaskBoundaryAllowed(filePath string) bool {
	taskID := tracker.GetTaskID()
	if taskID == "" {
		return false
	}

	taskFile := taskDocPath(taskID)
	data, err := os.ReadFile(taskFile)
	if err != nil {
		return false
	}

	content := string(data)
	if !strings.Contains(content, "允许新增文件目录") {
		return false
	}

	lines := strings.Split(content, "\n")
	for _, line := range lines {
		if strings.Contains(line, "允许新增文件目录") {
			parts := strings.SplitN(line, "：", 2)
			if len(parts) < 2 {
				parts = strings.SplitN(line, ":", 2)
			}
			if len(parts) < 2 {
				continue
			}
			allowedDirs := strings.Split(parts[1], ",")
			for _, dir := range allowedDirs {
				dir = strings.TrimSpace(dir)
				dir = strings.TrimRight(dir, "/") + "/"
				if strings.HasPrefix(filePath, dir) || strings.TrimRight(filePath, "/") == strings.TrimRight(dir, "/") {
					return true
				}
			}
		}
	}
	return false
}
