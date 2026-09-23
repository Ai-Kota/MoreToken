// generate-settings: Generate .claude/settings.json based on maturity level
// Usage: generate-settings <level>
//   level: 1 (宽松) | 2 (标准) | 3 (严格)
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

type Settings struct {
	Permissions Permissions `json:"permissions"`
	Hooks       Hooks       `json:"hooks"`
}

type Permissions struct {
	Allow []string `json:"allow"`
	Deny  []string `json:"deny"`
	Ask   []string `json:"ask"`
}

type Hooks struct {
	PreToolUse   []HookEntry `json:"PreToolUse"`
	PostToolUse  []HookEntry `json:"PostToolUse"`
	SessionStart []HookEntry `json:"SessionStart"`
}

type HookEntry struct {
	Matcher string      `json:"matcher"`
	Hooks   []HookCmd   `json:"hooks"`
}

type HookCmd struct {
	Type    string `json:"type"`
	Command string `json:"command"`
}

func runGenerateSettings(args []string) {
	if len(args) < 1 {
		fmt.Println("Usage: ai-guard generate-settings <level>")
		fmt.Println("  level: 1 (宽松) | 2 (标准) | 3 (严格)")
		osExit(1)
	}

	level := args[0]
	if level != "1" && level != "2" && level != "3" {
		fmt.Println("Error: level must be 1, 2, or 3")
		osExit(1)
	}

	settings := buildSettings(level)

	// Write to .claude/settings.json
	settingsDir := ".claude"
	os.MkdirAll(settingsDir, 0755)

	outputPath := filepath.Join(settingsDir, "settings.json")
	data, _ := json.MarshalIndent(settings, "", "  ")
	if err := os.WriteFile(outputPath, data, 0644); err != nil {
		fmt.Fprintf(os.Stderr, "❌ Failed to write settings: %v\n", err)
		osExit(1)
	}

	fmt.Printf("✅ settings.json 已生成 (Level %s)\n", level)
	fmt.Printf("   → %s\n", outputPath)
	fmt.Println()

	switch level {
	case "1":
		fmt.Println("宽松模式：仅环境保护 + 先读后写 + 读取追踪")
		fmt.Println("适用于：项目初期，代码和文档尚不完善")
	case "2":
		fmt.Println("标准模式：+ 变更范围检查")
		fmt.Println("适用于：项目有基本结构，可开始约束代码变更范围")
	case "3":
		fmt.Println("严格模式：+ 设计文档检查（全量约束）")
		fmt.Println("适用于：项目成熟，所有模块有设计文档")
	}
}

func buildSettings(level string) Settings {
	// Common permissions (same for all levels)
	permissions := Permissions{
		Allow: []string{
			"Read(**)",
			"Glob(**)",
			"Grep(**)",
			"Task(**)",
			"WebFetch(domain:*)",
			"Write(src/**)",
			"Write(tests/**)",
			"Write(docs/**)",
			"Write(templates/**)",
			"Write(*.md)",
			"Edit(src/**)",
			"Edit(tests/**)",
			"Edit(docs/**)",
			"Bash(go test *)",
			"Bash(go test -cover *)",
			"Bash(go build *)",
			"Bash(go run *)",
			"Bash(go vet *)",
			"Bash(go fmt *)",
			"Bash(go mod *)",
			"Bash(golangci-lint *)",
			"Bash(npm test *)",
			"Bash(npm run *)",
			"Bash(npm install *)",
			"Bash(npm ci *)",
			"Bash(yarn install *)",
			"Bash(pytest *)",
			"Bash(python *)",
			"Bash(python3 *)",
			"Bash(pip install *)",
			"Bash(pip3 install *)",
			"Bash(cargo test *)",
			"Bash(cargo build *)",
			"Bash(cargo run *)",
			"Bash(cargo clippy *)",
			"Bash(cargo fmt *)",
			"Bash(cargo add *)",
			"Bash(poetry add *)",
			"Bash(make *)",
			"Bash(git status *)",
			"Bash(git diff *)",
			"Bash(git log *)",
			"Bash(git add *)",
			"Bash(git commit *)",
			"Bash(git push *)",
			"Bash(git stash *)",
			"Bash(git stash pop *)",
			"Bash(git checkout -- *)",
			"Bash(git branch *)",
			"Bash(git merge *)",
			"Bash(find *)",
			"Bash(ls *)",
			"Bash(cat *)",
			"Bash(head *)",
			"Bash(tail *)",
			"Bash(wc *)",
			"Bash(grep *)",
			"Bash(sed *)",
			"Bash(awk *)",
			"Bash(sort *)",
			"Bash(uniq *)",
			"Bash(cut *)",
			"Bash(tr *)",
			"Bash(mkdir *)",
			"Bash(touch *)",
			"Bash(cp *)",
			"Bash(mv *)",
			"Bash(echo *)",
			"Bash(date *)",
			"Bash(pwd)",
			"Bash(whoami)",
			"Bash(env)",
			"Bash(./scripts/bin/*)",
			"Bash(scripts/bin/*)",
		},
		Deny: []string{
			"Write(.env*)",
			"Write(.secret*)",
			"Write(*.pem)",
			"Write(*.key)",
			"Write(deploy/**)",
			"Bash(rm -rf *)",
			"Bash(curl * | sh)",
			"Bash(wget * | sh)",
			"Bash(sudo *)",
			"Bash(chmod 777 *)",
			"Bash(chown *)",
			"Bash(git push --force*)",
			"Bash(git push -f*)",
			"Bash(git reset --hard HEAD*)",
			"Bash(git clean -fd*)",
			"Bash(terraform destroy*)",
			"Bash(terraform apply -auto-approve*)",
			"Bash(kubectl delete *)",
			"Bash(helm uninstall *)",
			"Bash(docker rm *)",
			"Bash(docker run --privileged*)",
			"Bash(apt install*)",
			"Bash(apt remove*)",
			"Bash(brew install*)",
			"Bash(brew uninstall*)",
			"Bash(npm install -g*)",
			"Bash(yarn global add*)",
			"Bash(go install*)",
			"Bash(cargo install*)",
			"Bash(pip install --user*)",
			"Bash(crontab*)",
			"Bash(systemctl*)",
			"Bash(service *)",
			"Write(.bashrc)",
			"Write(.zshrc)",
			"Write(.profile)",
			"Write(.bash_profile)",
			"Write(.config/fish/config.fish)",
		},
		Ask: []string{
			"Bash(git rebase *)",
			"Bash(docker build *)",
			"Bash(docker run *)",
			"Bash(docker-compose *)",
			"Bash(kubectl apply *)",
			"Bash(terraform plan *)",
			"Bash(terraform apply *)",
		},
	}

	// Base hooks (same for all levels)
	basePreToolUse := []HookEntry{
		{
			Matcher: "Write|Edit",
			Hooks:   []HookCmd{{Type: "command", Command: "bash scripts/hook.sh env-protect"}},
		},
		{
			Matcher: "Write|Edit",
			Hooks:   []HookCmd{{Type: "command", Command: "bash scripts/hook.sh check-read-before-write"}},
		},
	}

	basePostToolUse := []HookEntry{
		{
			Matcher: "Read",
			Hooks:   []HookCmd{{Type: "command", Command: "bash scripts/hook.sh track-read"}},
		},
		{
			Matcher: "Write|Edit",
			Hooks:   []HookCmd{{Type: "command", Command: "bash scripts/hook.sh clear-read"}},
		},
	}

	// Level-specific hooks
	var levelHooks []HookEntry

	switch level {
	case "1":
		// 宽松：仅环境保护 + 先读后写
		levelHooks = []HookEntry{}

	case "2":
		// 标准：+ check-scope
		levelHooks = []HookEntry{
			{
				Matcher: "Write|Edit",
				Hooks:   []HookCmd{{Type: "command", Command: "bash scripts/hook.sh check-scope"}},
			},
		}

	case "3":
		// 严格：+ check-scope + check-design-doc
		levelHooks = []HookEntry{
			{
				Matcher: "Write|Edit",
				Hooks:   []HookCmd{{Type: "command", Command: "bash scripts/hook.sh check-scope"}},
			},
			{
				Matcher: "Write",
				Hooks:   []HookCmd{{Type: "command", Command: "bash scripts/hook.sh check-design-doc"}},
			},
		}
	}

	// Bash enforcement (all levels): env-protect + wu-guard + verify-commit.
	// These are baseline flow constraints — level progression only gates
	// check-scope (L2) and check-design-doc (L3).
	allPreToolUse := append(basePreToolUse, levelHooks...)
	allPreToolUse = append(allPreToolUse, HookEntry{
		Matcher: "Bash",
		Hooks: []HookCmd{
			{Type: "command", Command: "bash scripts/hook.sh env-protect"},
			{Type: "command", Command: "bash scripts/hook.sh wu-guard"},
			{Type: "command", Command: "bash scripts/hook.sh verify-commit"},
			{Type: "command", Command: "bash scripts/hook.sh check-bash-write"},
		},
	})

	return Settings{
		Permissions: permissions,
		Hooks: Hooks{
			PreToolUse:  allPreToolUse,
			PostToolUse: basePostToolUse,
			SessionStart: []HookEntry{
				{
					Matcher: "startup|resume",
					Hooks:   []HookCmd{{Type: "command", Command: "bash scripts/hook.sh session-bootstrap"}},
				},
			},
		},
	}
}
