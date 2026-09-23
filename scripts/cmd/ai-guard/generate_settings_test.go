package main

import (
	"strings"
	"testing"
)

func TestBuildSettingsBase(t *testing.T) {
	for _, level := range []string{"1", "2", "3"} {
		s := buildSettings(level)
		if len(s.Permissions.Allow) == 0 || len(s.Permissions.Deny) == 0 {
			t.Errorf("level %s should have allow+deny permissions", level)
		}
		// Base pre-tool hooks: env-protect + check-read-before-write (Write|Edit) + bash env-protect
		if len(s.Hooks.PreToolUse) < 3 {
			t.Errorf("level %s PreToolUse hooks = %d, want >= 3", level, len(s.Hooks.PreToolUse))
		}
		// Base post-tool hooks: track-read + clear-read
		if len(s.Hooks.PostToolUse) != 2 {
			t.Errorf("level %s PostToolUse hooks = %d, want 2", level, len(s.Hooks.PostToolUse))
		}
	}
}

func TestBuildSettingsLevelDifferences(t *testing.T) {
	level1 := buildSettings("1")
	level2 := buildSettings("2")
	level3 := buildSettings("3")

	hookCount := func(s Settings) int { return len(s.Hooks.PreToolUse) }
	if hookCount(level1) >= hookCount(level2) {
		t.Errorf("level 2 should add hooks over level 1: %d vs %d", hookCount(level1), hookCount(level2))
	}
	if hookCount(level2) >= hookCount(level3) {
		t.Errorf("level 3 should add hooks over level 2: %d vs %d", hookCount(level2), hookCount(level3))
	}

	// Level 2+ has check-scope; level 3 adds check-design-doc.
	commands := func(s Settings) []string {
		var out []string
		for _, h := range s.Hooks.PreToolUse {
			for _, c := range h.Hooks {
				out = append(out, c.Command)
			}
		}
		return out
	}
	has := func(cmds []string, sub string) bool {
		for _, c := range cmds {
			if strings.Contains(c, sub) {
				return true
			}
		}
		return false
	}

	if has(commands(level1), "check-scope") {
		t.Error("level 1 must not include check-scope")
	}
	if !has(commands(level2), "check-scope") {
		t.Error("level 2 should include check-scope")
	}
	if has(commands(level2), "check-design-doc") {
		t.Error("level 2 must not include check-design-doc")
	}
	if !has(commands(level3), "check-design-doc") {
		t.Error("level 3 should include check-design-doc")
	}
}

func TestBuildSettingsBaselineHooks(t *testing.T) {
	// Baseline flow hooks must exist at every level: wu-guard / verify-commit
	// (Bash) and session-bootstrap (SessionStart). Level progression only gates
	// check-scope (L2) and check-design-doc (L3).
	all := func(s Settings) []string {
		var out []string
		collect := func(entries []HookEntry) {
			for _, h := range entries {
				for _, c := range h.Hooks {
					out = append(out, c.Command)
				}
			}
		}
		collect(s.Hooks.PreToolUse)
		collect(s.Hooks.PostToolUse)
		collect(s.Hooks.SessionStart)
		return out
	}
	contains := func(cmds []string, sub string) bool {
		for _, c := range cmds {
			if strings.Contains(c, sub) {
				return true
			}
		}
		return false
	}

	for _, level := range []string{"1", "2", "3"} {
		cmds := all(buildSettings(level))
		for _, want := range []string{"wu-guard", "verify-commit", "session-bootstrap"} {
			if !contains(cmds, want) {
				t.Errorf("level %s missing baseline hook %q\ncmds: %v", level, want, cmds)
			}
		}
		if len(buildSettings(level).Hooks.SessionStart) != 1 {
			t.Errorf("level %s should have exactly one SessionStart hook", level)
		}
	}
}
