// Shared types and utilities for all ai-guard subcommands.
package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// ToolInput represents the JSON input from Claude Code hooks.
// Claude Code sends tool arguments under "tool_input"; "input" is kept for
// legacy/test payloads. readHookInput normalizes both into Input so all
// consumers read one field. Values that are not strings (e.g. nested objects
// in Edit's tool_input) are dropped rather than failing the parse.
type ToolInput struct {
	ToolName string            `json:"tool_name"`
	Input    map[string]string `json:"-"`
}

// readHookInput reads and parses JSON tool input from stdin (for Hook subcommands).
func readHookInput() (ToolInput, error) {
	data, err := io.ReadAll(os.Stdin)
	if err != nil {
		return ToolInput{}, err
	}
	var raw map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		return ToolInput{}, err
	}
	input := ToolInput{Input: map[string]string{}}
	if n, ok := raw["tool_name"].(string); ok {
		input.ToolName = n
	}
	// Real payloads use tool_input; fall back to input (legacy/test).
	params, _ := raw["tool_input"].(map[string]any)
	if params == nil {
		params, _ = raw["input"].(map[string]any)
	}
	for k, v := range params {
		if s, ok := v.(string); ok {
			input.Input[k] = s
		}
	}
	return input, nil
}

// extractFilePath extracts the file path from hook input (tries common field names).
func extractFilePath(input ToolInput) string {
	for _, key := range []string{"file_path", "path", "filePath"} {
		if v, ok := input.Input[key]; ok && v != "" {
			return v
		}
	}
	return ""
}

// fileExists checks if a file or directory exists.
func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// dirExists checks if a directory exists.
func dirExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}

// readFile reads a file and returns its content as a string.
func readFile(path string) (string, error) {
	data, err := os.ReadFile(path)
	return string(data), err
}

// detectProjectType detects the project type based on marker files.
func detectProjectType() string {
	if fileExists("go.mod") {
		return "go"
	}
	if fileExists("package.json") {
		return "node"
	}
	if fileExists("pyproject.toml") || fileExists("requirements.txt") {
		return "python"
	}
	if fileExists("Cargo.toml") {
		return "rust"
	}
	return "unknown"
}

// findTaskLine finds the line in content that contains the task ID.
func findTaskLine(content, taskID string) string {
	lines := strings.Split(content, "\n")
	for _, line := range lines {
		if strings.Contains(line, taskID) {
			return line
		}
	}
	return ""
}

// normalizePath converts a path to forward slashes and cleans it.
func normalizePath(p string) string {
	return filepath.ToSlash(filepath.Clean(p))
}

// taskDocPath returns the per-task definition file path for a task ID,
// tolerating both the "T-101" and bare "101" forms so consumers that read the
// task ID from the tracker (which stores the full "T-101" form) don't double
// the prefix.
func taskDocPath(taskID string) string {
	id := strings.TrimSpace(taskID)
	if !strings.HasPrefix(strings.ToUpper(id), "T-") {
		id = "T-" + id
	}
	return fmt.Sprintf("docs/tasks/%s.md", id)
}

// findFilesByExt recursively finds files matching extensions under a base directory.
func findFilesByExt(baseDir string, extensions []string) []string {
	var files []string
	filepath.WalkDir(baseDir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			return nil
		}
		for _, ext := range extensions {
			if strings.HasSuffix(path, ext) {
				files = append(files, path)
				break
			}
		}
		return nil
	})
	return files
}
