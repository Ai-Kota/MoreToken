// clear-read: PostToolUse Hook
// Removes a file from the read tracking list after a Write/Edit operation.
package main

import (
	"os"
	"path/filepath"

	"spt/scripts/internal/tracker"
)

func runClearRead(args []string) {
	input, err := readHookInput()
	if err != nil {
		osExit(0)
	}

	filePath := extractFilePath(input)
	if filePath == "" {
		osExit(0)
	}

	// Convert absolute paths to relative to match tracker storage
	absPath := filepath.Clean(filePath)
	if filepath.IsAbs(absPath) {
		cwd, err := os.Getwd()
		if err == nil {
			if rel, err := filepath.Rel(cwd, absPath); err == nil {
				absPath = rel
			}
		}
	}

	tracker.RemoveFile(absPath)
}
