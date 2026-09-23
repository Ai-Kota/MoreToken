// Package tracker provides cross-platform file read tracking for Claude Code hooks.
// It uses JSON for portability and platform-safe file locking.
package tracker

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"time"
)

const TrackerFile = ".claude/read_tracker.json"

// TrackerData holds the state of read file tracking plus the active task
// context (intent gate) that PreToolUse hooks enforce against.
type TrackerData struct {
	ReadFiles     []string `json:"read_files"`
	TaskID        string   `json:"task_id"`
	AllowedFiles  []string `json:"allowed_files,omitempty"`
	DeniedFiles   []string `json:"denied_files,omitempty"`
	NewFileDirs   []string `json:"new_file_dirs,omitempty"`
	DoD           []string `json:"dod,omitempty"`
	TaskStartedAt string   `json:"task_started_at,omitempty"`
}

var (
	fileMu sync.Mutex
)

// EnsureDir creates the .claude directory if it doesn't exist.
func EnsureDir() error {
	dir := filepath.Dir(TrackerFile)
	if dir == "" {
		dir = "."
	}
	return os.MkdirAll(dir, 0755)
}

// Load reads the tracker JSON file. Returns empty tracker on error.
func Load() TrackerData {
	EnsureDir()

	data, err := os.ReadFile(TrackerFile)
	if err != nil {
		return TrackerData{ReadFiles: []string{}, TaskID: ""}
	}

	var t TrackerData
	if err := json.Unmarshal(data, &t); err != nil {
		return TrackerData{ReadFiles: []string{}, TaskID: ""}
	}

	if t.ReadFiles == nil {
		t.ReadFiles = []string{}
	}
	return t
}

// Save writes the tracker JSON file atomically.
func Save(t TrackerData) error {
	EnsureDir()

	data, err := json.MarshalIndent(t, "", "  ")
	if err != nil {
		return err
	}

	// Atomic write: write to temp file then rename
	tmp := TrackerFile + ".tmp"
	if err := os.WriteFile(tmp, data, 0644); err != nil {
		return err
	}

	// On Windows, rename fails if target exists, so remove first
	if runtime.GOOS == "windows" {
		os.Remove(TrackerFile)
	}

	return os.Rename(tmp, TrackerFile)
}

// AddFile marks a file as read.
func AddFile(filePath string) {
	fileMu.Lock()
	defer fileMu.Unlock()

	t := Load()
	t.ReadFiles = uniqueAppend(t.ReadFiles, normalize(filePath))

	// Also add parent directory for new file creation allowance
	parent := filepath.Dir(filePath)
	if parent != "" && parent != "." {
		t.ReadFiles = uniqueAppend(t.ReadFiles, normalize(parent))
	}

	Save(t)
}

// RemoveFile removes a file from the read tracking list.
func RemoveFile(filePath string) {
	fileMu.Lock()
	defer fileMu.Unlock()

	t := Load()
	t.ReadFiles = remove(t.ReadFiles, normalize(filePath))
	Save(t)
}

// IsRead checks if a file has been read.
func IsRead(filePath string) bool {
	t := Load()
	norm := normalize(filePath)
	for _, f := range t.ReadFiles {
		if f == norm {
			return true
		}
		// Check if parent directory was read
		if filepath.Dir(norm) == f {
			return true
		}
	}
	return false
}

// SetTaskID sets the current task ID in the tracker.
func SetTaskID(taskID string) {
	fileMu.Lock()
	defer fileMu.Unlock()

	t := Load()
	t.TaskID = taskID
	Save(t)
}

// GetTaskID returns the current task ID.
func GetTaskID() string {
	t := Load()
	return t.TaskID
}

// BeginTask records the active task context (intent gate). This is the single
// way an active task becomes known to the PreToolUse hooks; without it,
// check-scope blocks project-file writes.
func BeginTask(taskID string, allowed, denied, newDirs, dod []string) {
	fileMu.Lock()
	defer fileMu.Unlock()

	t := Load()
	t.TaskID = taskID
	t.AllowedFiles = allowed
	t.DeniedFiles = denied
	t.NewFileDirs = newDirs
	t.DoD = dod
	t.TaskStartedAt = time.Now().Format(time.RFC3339)
	Save(t)
}

// ClearTask removes the active task context (end-task / stale-task cleanup).
func ClearTask() {
	fileMu.Lock()
	defer fileMu.Unlock()

	t := Load()
	t.TaskID = ""
	t.AllowedFiles = nil
	t.DeniedFiles = nil
	t.NewFileDirs = nil
	t.DoD = nil
	t.TaskStartedAt = ""
	Save(t)
}

// GetTaskContext returns the full active task context (empty values if none).
func GetTaskContext() TrackerData {
	t := Load()
	if t.AllowedFiles == nil {
		t.AllowedFiles = []string{}
	}
	if t.DeniedFiles == nil {
		t.DeniedFiles = []string{}
	}
	if t.NewFileDirs == nil {
		t.NewFileDirs = []string{}
	}
	if t.DoD == nil {
		t.DoD = []string{}
	}
	return t
}

// normalize converts path to forward slashes for cross-platform consistency.
func normalize(p string) string {
	return filepath.ToSlash(filepath.Clean(p))
}

// uniqueAppend adds a string to a slice if not already present.
func uniqueAppend(slice []string, s string) []string {
	for _, v := range slice {
		if v == s {
			return slice
		}
	}
	return append(slice, s)
}

// remove removes a string from a slice.
func remove(slice []string, s string) []string {
	result := make([]string, 0, len(slice))
	for _, v := range slice {
		if v != s {
			result = append(result, v)
		}
	}
	return result
}
