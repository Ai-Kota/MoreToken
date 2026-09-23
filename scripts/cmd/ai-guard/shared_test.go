package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDetectProjectType(t *testing.T) {
	tests := []struct {
		name     string
		files    map[string]string
		expected string
	}{
		{
			name: "go project",
			files: map[string]string{
				"go.mod": "",
			},
			expected: "go",
		},
		{
			name: "node project",
			files: map[string]string{
				"package.json": "",
			},
			expected: "node",
		},
		{
			name: "python project with pyproject.toml",
			files: map[string]string{
				"pyproject.toml": "",
			},
			expected: "python",
		},
		{
			name: "python project with requirements.txt",
			files: map[string]string{
				"requirements.txt": "",
			},
			expected: "python",
		},
		{
			name: "rust project",
			files: map[string]string{
				"Cargo.toml": "",
			},
			expected: "rust",
		},
		{
			name:     "unknown project",
			files:    map[string]string{},
			expected: "unknown",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tmpDir := t.TempDir()
			origWd, _ := os.Getwd()
			os.Chdir(tmpDir)
			defer os.Chdir(origWd)

			for name, content := range tt.files {
				if err := os.WriteFile(name, []byte(content), 0644); err != nil {
					t.Fatalf("failed to create file %s: %v", name, err)
				}
			}

			result := detectProjectType()
			if result != tt.expected {
				t.Errorf("expected %s, got %s", tt.expected, result)
			}
		})
	}
}

func TestFileExists(t *testing.T) {
	tmpDir := t.TempDir()
	existingFile := filepath.Join(tmpDir, "exists.txt")
	if err := os.WriteFile(existingFile, []byte("hello"), 0644); err != nil {
		t.Fatalf("failed to create test file: %v", err)
	}

	if !fileExists(existingFile) {
		t.Errorf("fileExists should return true for existing file")
	}

	nonExisting := filepath.Join(tmpDir, "notexists.txt")
	if fileExists(nonExisting) {
		t.Errorf("fileExists should return false for non-existing file")
	}
}

func TestDirExists(t *testing.T) {
	tmpDir := t.TempDir()
	if !dirExists(tmpDir) {
		t.Errorf("dirExists should return true for existing directory")
	}

	nonExisting := filepath.Join(tmpDir, "notexists")
	if dirExists(nonExisting) {
		t.Errorf("dirExists should return false for non-existing directory")
	}
}

func TestNormalizePath(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"src/foo/bar.go", "src/foo/bar.go"},
		{"src\\foo\\bar.go", "src/foo/bar.go"},
		{"src//foo//bar.go", "src/foo/bar.go"},
		{"./src/foo/bar.go", "src/foo/bar.go"},
		{"../src/foo/bar.go", "../src/foo/bar.go"},
	}

	for _, tt := range tests {
		result := normalizePath(tt.input)
		if result != tt.expected {
			t.Errorf("normalizePath(%q) = %q, expected %q", tt.input, result, tt.expected)
		}
	}
}

func TestFindTaskLine(t *testing.T) {
	content := `# Tasks
| ID | Task | Status |
| T-001 | Initialize | ✅ |
| T-002 | Build | ⬜ |
| T-003 | Deploy | 🔄 |`

	tests := []struct {
		taskID   string
		expected string
	}{
		{"T-001", "| T-001 | Initialize | ✅ |"},
		{"T-002", "| T-002 | Build | ⬜ |"},
		{"T-003", "| T-003 | Deploy | 🔄 |"},
		{"T-999", ""},
	}

	for _, tt := range tests {
		result := findTaskLine(content, tt.taskID)
		if result != tt.expected {
			t.Errorf("findTaskLine(%q) = %q, expected %q", tt.taskID, result, tt.expected)
		}
	}
}

func TestExtractFilePath(t *testing.T) {
	tests := []struct {
		name     string
		input    ToolInput
		expected string
	}{
		{
			name: "file_path field",
			input: ToolInput{
				Input: map[string]string{
					"file_path": "src/main.go",
				},
			},
			expected: "src/main.go",
		},
		{
			name: "path field",
			input: ToolInput{
				Input: map[string]string{
					"path": "docs/README.md",
				},
			},
			expected: "docs/README.md",
		},
		{
			name: "filePath field",
			input: ToolInput{
				Input: map[string]string{
					"filePath": "config.yaml",
				},
			},
			expected: "config.yaml",
		},
		{
			name: "no matching field",
			input: ToolInput{
				Input: map[string]string{
					"other": "value",
				},
			},
			expected: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := extractFilePath(tt.input)
			if result != tt.expected {
				t.Errorf("extractFilePath() = %q, expected %q", result, tt.expected)
			}
		})
	}
}

func TestTaskDocPath(t *testing.T) {
	tests := []struct {
		name   string
		taskID string
		want   string
	}{
		{"with-prefix", "T-101", "docs/tasks/T-101.md"},
		{"bare-digits", "101", "docs/tasks/T-101.md"},
		{"lowercase-prefix", "t-202", "docs/tasks/t-202.md"},
		{"with-space", " T-101 ", "docs/tasks/T-101.md"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := taskDocPath(tt.taskID); got != tt.want {
				t.Errorf("taskDocPath(%q) = %q, want %q", tt.taskID, got, tt.want)
			}
		})
	}
}