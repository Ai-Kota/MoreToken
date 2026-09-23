// env-protect: PreToolUse Hook
// Protects system environment from AI damage.
// Blocks writes to system locations, shell configs, Claude Code configs, etc.
// Exit 0 = allow, exit 1 = block.
package main

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// Protected paths (normalized)
var protectedPaths = []string{
	// Shell configs
	".bashrc", ".bash_profile", ".bash_login", ".bash_logout",
	".zshrc", ".zshenv", ".zprofile", ".zlogin",
	".profile", ".login", ".logout",
	".config/fish/config.fish",

	// Claude Code configs (only human should modify)
	".claude/settings.json",
	".claude/settings.local.json",

	// System directories
	"/etc/", "/usr/", "/bin/", "/sbin/", "/lib/", "/opt/",
	"/System/", "/Library/",

	// Windows system
	"C:\\Windows\\", "C:\\Program Files\\", "C:\\ProgramData\\",

	// Package manager configs
	".npmrc", ".yarnrc", ".pypirc", ".cargo/config.toml",

	// Git global configs
	".gitconfig", ".gitignore_global",

	// SSH/keys
	".ssh/", ".gnupg/", ".aws/", ".azure/", ".config/gcloud/",

	// Crontab/systemd
	".config/systemd/",

	// Environment files (should not be created by AI)
	".env.local", ".env.production", ".env.development",
}

func runEnvProtect(args []string) {
	input, err := readHookInput()
	if err != nil {
		osExit(0)
	}

	filePath := extractFilePath(input)
	if filePath == "" {
		// Check for Bash commands
		checkBashCommand(input)
		osExit(0)
	}

	filePath = filepath.ToSlash(filepath.Clean(filePath))
	homeDir := getHomeDir()

	// Check against protected paths
	for _, protected := range protectedPaths {
		// Absolute path match
		if strings.HasPrefix(filePath, filepath.ToSlash(protected)) {
			envBlock(filePath, fmt.Sprintf("系统保护路径: %s", protected))
		}

		// Home directory relative match
		homePath := filepath.ToSlash(filepath.Join(homeDir, protected))
		if strings.HasPrefix(filePath, homePath) || filePath == filepath.ToSlash(homePath) {
			envBlock(filePath, fmt.Sprintf("用户配置保护: ~/%s", protected))
		}

		// Direct filename match
		base := filepath.Base(filePath)
		if base == filepath.Base(protected) {
			envBlock(filePath, fmt.Sprintf("保护文件: %s", protected))
		}
	}

	// Block writes to project root config files
	if isInProjectRoot(filePath) && isConfigFile(filePath) {
		envBlock(filePath, "项目根目录配置文件（需人工修改）")
	}

	osExit(0)
}

func checkBashCommand(input ToolInput) {
	cmd, ok := input.Input["command"]
	if !ok {
		return
	}

	cmd = strings.ToLower(strings.TrimSpace(cmd))

	// Block dangerous commands
	dangerous := []string{
		"sudo ", "su ", "chmod 777", "chown ",
		"crontab -e", "crontab -r",
		"systemctl ", "service ",
		"apt install", "apt remove", "apt purge",
		"brew install", "brew uninstall",
		"yum install", "yum remove",
		"dnf install", "dnf remove",
		"pacman -S", "pacman -R",
		"winget install", "winget uninstall",
		"choco install", "choco uninstall",
		"npm install -g", "yarn global add",
		"pip install --user", "pip3 install --user",
		"go install", "cargo install",
		"docker run --privileged",
		"kubectl delete", "helm uninstall",
		"terraform destroy",
		"git push --force", "git push -f",
		"git reset --hard", "git clean -fd",
		"rm -rf /", "rm -rf ~",
		"rmdir /s /q",
		"format ", "mkfs.",
		"dd if=", "dd of=",
	}

	for _, d := range dangerous {
		if strings.Contains(cmd, d) {
			envBlock(cmd, fmt.Sprintf("危险命令: %s", d))
		}
	}
}

func envBlock(target, reason string) {
	fmt.Fprintf(os.Stderr, "🛑 环境保护拦截: %s\n"+
		"   原因: %s\n"+
		"   此操作可能损坏系统环境，请联系人工处理。\n",
		target, reason)
	osExit(1)
}

func getHomeDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		if runtime.GOOS == "windows" {
			return os.Getenv("USERPROFILE")
		}
		return os.Getenv("HOME")
	}
	return home
}

func isInProjectRoot(filePath string) bool {
	cwd, err := os.Getwd()
	if err != nil {
		return false
	}
	rel, err := filepath.Rel(cwd, filePath)
	if err != nil {
		return false
	}
	return !strings.Contains(rel, "..") && !strings.Contains(string(os.PathSeparator), rel)
}

func isConfigFile(filePath string) bool {
	configExts := []string{".json", ".yaml", ".yml", ".toml", ".ini", ".cfg", ".conf"}
	base := filepath.Base(filePath)

	for _, ext := range configExts {
		if strings.HasSuffix(strings.ToLower(base), ext) {
			return true
		}
	}

	blockList := []string{
		".eslintrc", ".prettierrc", ".editorconfig",
		"tsconfig.json", "webpack.config", "vite.config",
		"makefile", "dockerfile", "docker-compose",
		".env", ".env.",
	}
	for _, b := range blockList {
		if strings.Contains(strings.ToLower(base), b) {
			return true
		}
	}

	return false
}
