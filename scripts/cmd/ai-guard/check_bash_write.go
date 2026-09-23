// check-bash-write: PreToolUse Hook on Bash.
// Detects file-writing bash commands (redirects, tee, sed -i, touch, cp/mv) and
// enforces the same write-scope gate as check-scope, closing the gap where
// writes issued via Bash (echo >, sed -i, ...) bypass the Write|Edit hooks.
// Exit 0 = allow, exit 2 = block.
package main

import (
	"fmt"
	"os"
	"regexp"
	"strings"
)

func runCheckBashWrite(args []string) {
	input, err := readHookInput()
	if err != nil {
		// Fail-closed: cannot parse the command, so block.
		fmt.Fprintf(os.Stderr, "[check-bash-write] ❌ hook 输入解析失败，拒绝（fail-closed）\n")
		osExit(2)
	}
	if input.ToolName != "Bash" {
		osExit(0)
	}
	command := input.Input["command"]
	for _, t := range parseBashWriteTargets(command) {
		if code := enforceWriteScope(t); code != 0 {
			fmt.Fprintf(os.Stderr, "[check-bash-write] 命令写入 %s 被拦截（exit %d）\n", t, code)
			osExit(2)
		}
	}
	osExit(0)
}

// parseBashWriteTargets extracts likely file-write target paths from a bash
// command. Heuristic (favor precision over recall to avoid false blocks):
//   - > / >> redirects (echo/cat/printf/awk ... > file)
//   - tee file
//   - sed -i <script> file   (file = last token after -i)
//   - touch file
//   - cp / mv <src> <dst>    (dst = last token)
func parseBashWriteTargets(command string) []string {
	var targets []string
	add := func(p string) {
		p = strings.TrimSpace(strings.Trim(p, "'\""))
		if p == "" || isSystemPath(p) {
			return
		}
		targets = append(targets, p)
	}

	// Strip quoted spans so a ">" inside a message (e.g. git commit -m) is not
	// mistaken for a redirect.
	unquoted := regexp.MustCompile(`["'][^"']*["']`).ReplaceAllString(command, "")

	// Strip sed -i command spans: ">" inside a sed replacement (e.g.
	// s|text (25+ 子命令)|...) is not a redirect.
	noSed := regexp.MustCompile(`\bsed\s+-i[^;|&]*`).ReplaceAllString(unquoted, " ")

	// Redirect target: anything after > or >>.
	for _, m := range regexp.MustCompile(`[>]{1,2}\s*["']?([^"'\s;|&]+)`).FindAllStringSubmatch(noSed, -1) {
		if len(m) > 1 {
			add(m[1])
		}
	}
	// tee/touch/cp/mv/sed: token-based operand extraction.
	words := strings.Fields(unquoted)
	isCmdSep := func(w string) bool {
		switch w {
		case "<", ">", ">>", "&&", "||", ";", "|", "(", ")":
			return true
		}
		return false
	}
	isOperand := func(w string) bool {
		return w != "" && !strings.HasPrefix(w, "-") && !isCmdSep(w)
	}
	for i, w := range words {
		switch w {
		case "sed":
			// sed -i <script> file: target is the LAST operand (avoids the
			// regex misfiring on unquoted scripts that contain spaces, e.g.
			// s|text (25+ 子命令)|...).
			if i+1 < len(words) && words[i+1] != "-i" {
				continue
			}
			last := ""
			for j := i + 2; j < len(words); j++ {
				if isOperand(words[j]) {
					last = strings.Trim(words[j], "'\"")
				}
			}
			if last != "" {
				add(last)
			}
		case "tee":
			// tee writes to its FIRST operand (tee out.txt < in.txt → out.txt).
			for j := i + 1; j < len(words); j++ {
				if isCmdSep(words[j]) {
					break
				}
				if isOperand(words[j]) {
					add(words[j])
					break
				}
			}
		case "touch", "cp", "mv":
			// These write to the LAST non-flag operand before the next command
			// separator, so flags/values (touch -d <date>, cp -r <src>) and
			// later commands (cp a b && cp c d) don't get picked up.
			last := ""
			for j := i + 1; j < len(words); j++ {
				if isCmdSep(words[j]) {
					break
				}
				if isOperand(words[j]) {
					last = words[j]
				}
			}
			if last != "" {
				add(last)
			}
		}
	}
	return targets
}

// isSystemPath filters out non-project paths that redirects commonly touch.
func isSystemPath(p string) bool {
	lower := strings.ToLower(p)
	for _, prefix := range []string{"/dev/", "/proc/", "/sys/", "/tmp/", "/var/", "/etc/", "/usr/", "/dev/null", "nul"} {
		if lower == prefix || strings.HasPrefix(lower, prefix) {
			return true
		}
	}
	return false
}
