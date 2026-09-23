// check-design-doc: PreToolUse Hook
// Validates that a design document exists before writing to src/.
// Exit 0 = allow, exit 1 = block.
package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

func runCheckDesignDoc(args []string) {
	input, err := readHookInput()
	if err != nil {
		osExit(0)
	}

	filePath := extractFilePath(input)
	if filePath == "" {
		osExit(0)
	}

	filePath = filepath.ToSlash(filepath.Clean(filePath))

	// Only check src/ files
	if !strings.HasPrefix(filePath, "src/") {
		osExit(0)
	}

	// Extract module name from path
	parts := strings.Split(filePath, "/")
	if len(parts) < 3 {
		osExit(0)
	}

	moduleName := parts[1]

	// Check if design doc exists for this module
	exists, expectedPath := checkDesignDocExists(moduleName)
	if exists {
		osExit(0)
	}

	// Try sub-module name
	if len(parts) >= 4 && parts[2] != "" {
		subModule := parts[2]
		subExists, subExpected := checkDesignDocExists(moduleName + "-" + subModule)
		if subExists {
			osExit(0)
		}
		expectedPath = subExpected
	}

	fmt.Fprintf(os.Stderr, "ERROR: Writing to %s requires a module design document.\n"+
		"       Expected: %s\n"+
		"       Please create the design document first, or document the architecture in docs/architecture/ARCHITECTURE.md\n",
		filePath, expectedPath)
	osExit(1)
}

func checkDesignDocExists(moduleName string) (bool, string) {
	pattern := fmt.Sprintf("docs/architecture/modules/%s*.md", moduleName)
	matches, _ := filepath.Glob(pattern)
	if len(matches) > 0 {
		// Normalize separators so the returned path is consistent cross-platform.
		return true, normalizePath(matches[0])
	}
	expected := fmt.Sprintf("docs/architecture/modules/%s.md", moduleName)
	return false, expected
}
