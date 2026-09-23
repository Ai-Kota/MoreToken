// track-read: PostToolUse Hook
// Records that a file was read after a Read operation.
package main

import (
	"os"
	"path/filepath"

	"spt/scripts/internal/tracker"
)

func runTrackRead(args []string) {
	input, err := readHookInput()
	if err != nil {
		osExit(0)
	}

	filePath := extractFilePath(input)
	if filePath == "" {
		osExit(0)
	}

	// Convert absolute paths to relative paths to prevent
	// Windows drive letters (e.g. "E:") from creating garbage directories.
	absPath := filepath.Clean(filePath)
	if filepath.IsAbs(absPath) {
		cwd, err := os.Getwd()
		if err == nil {
			if rel, err := filepath.Rel(cwd, absPath); err == nil {
				absPath = rel
			}
		}
	}

	tracker.AddFile(absPath)
}
