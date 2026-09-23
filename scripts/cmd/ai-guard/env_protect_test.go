package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestIsInProjectRoot(t *testing.T) {
	tmpDir := t.TempDir()
	origWd, _ := os.Getwd()
	os.Chdir(tmpDir)
	defer os.Chdir(origWd)

	// Create test file in project root
	rootFile := filepath.Join(tmpDir, "config.json")
	os.WriteFile(rootFile, []byte("{}"), 0644)

	// Create test file in subdirectory
	subDir := filepath.Join(tmpDir, "src")
	os.MkdirAll(subDir, 0755)
	subFile := filepath.Join(subDir, "config.json")
	os.WriteFile(subFile, []byte("{}"), 0644)

	tests := []struct {
		name     string
		filePath string
		expected bool
	}{
		{
			name:     "file in project root",
			filePath: rootFile,
			expected: true,
		},
		{
			name:     "file in subdirectory",
			filePath: subFile,
			expected: true,
		},
		{
			name:     "file outside project",
			filePath: "/etc/passwd",
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := isInProjectRoot(tt.filePath)
			if result != tt.expected {
				t.Errorf("isInProjectRoot(%q) = %v, expected %v", tt.filePath, result, tt.expected)
			}
		})
	}
}

func TestIsConfigFile(t *testing.T) {
	tests := []struct {
		name     string
		filePath string
		expected bool
	}{
		{"json config", "config.json", true},
		{"yaml config", "config.yaml", true},
		{"yml config", "config.yml", true},
		{"toml config", "config.toml", true},
		{"ini config", "config.ini", true},
		{"cfg config", "config.cfg", true},
		{"conf config", "config.conf", true},
		{".eslintrc", ".eslintrc", true},
		{".prettierrc", ".prettierrc", true},
		{".editorconfig", ".editorconfig", true},
		{"tsconfig.json", "tsconfig.json", true},
		{"webpack.config", "webpack.config.js", true},
		{"vite.config", "vite.config.ts", true},
		{"Makefile", "Makefile", true},
		{"Dockerfile", "Dockerfile", true},
		{"docker-compose", "docker-compose.yml", true},
		{".env", ".env", true},
		{".env.local", ".env.local", true},
		{"regular go file", "main.go", false},
		{"regular txt file", "README.md", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := isConfigFile(tt.filePath)
			if result != tt.expected {
				t.Errorf("isConfigFile(%q) = %v, expected %v", tt.filePath, result, tt.expected)
			}
		})
	}
}

func TestGetHomeDir(t *testing.T) {
	home := getHomeDir()
	if home == "" {
		t.Error("getHomeDir() should not return empty string")
	}
}

func TestProtectedPaths(t *testing.T) {
	// Verify protectedPaths contains expected entries
	expectedPaths := []string{
		".bashrc", ".zshrc", ".profile",
		".claude/settings.json",
		"/etc/", "/usr/", "/bin/",
		".ssh/", ".gnupg/",
		".env.local",
	}

	for _, path := range expectedPaths {
		found := false
		for _, p := range protectedPaths {
			if p == path {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("protectedPaths missing expected path: %s", path)
		}
	}
}

func TestCheckBashCommandBlocked(t *testing.T) {
	chdirTemp(t)
	dangerous := []string{
		"sudo rm -rf /",
		"apt install nginx",
		"chmod 777 /etc/passwd",
		"npm install -g foo",
		"git push --force origin main",
		"rm -rf ~",
	}
	for _, cmd := range dangerous {
		code := runWithExitCapture(t, func() {
			checkBashCommand(ToolInput{ToolName: "Bash", Input: map[string]string{"command": cmd}})
		})
		if code != 1 {
			t.Errorf("checkBashCommand(%q) should block (exit 1), got %d", cmd, code)
		}
	}
}

func TestCheckBashCommandAllowed(t *testing.T) {
	chdirTemp(t)
	safe := []string{
		"go test ./...",
		"git status",
		"ls -la",
		"npm test",
	}
	for _, cmd := range safe {
		code := runWithExitCapture(t, func() {
			checkBashCommand(ToolInput{ToolName: "Bash", Input: map[string]string{"command": cmd}})
		})
		if code != -1 {
			t.Errorf("checkBashCommand(%q) should allow (no exit), got %d", cmd, code)
		}
	}
}

func TestCheckBashCommandNoCommand(t *testing.T) {
	chdirTemp(t)
	code := runWithExitCapture(t, func() {
		checkBashCommand(ToolInput{ToolName: "Bash", Input: map[string]string{}})
	})
	if code != -1 {
		t.Errorf("missing command should not exit, got %d", code)
	}
}

func TestEnvBlock(t *testing.T) {
	chdirTemp(t)
	code := runWithExitCapture(t, func() {
		envBlock("sudo x", "危险命令: sudo ")
	})
	if code != 1 {
		t.Errorf("envBlock should exit 1, got %d", code)
	}
}