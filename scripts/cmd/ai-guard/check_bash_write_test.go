package main

import (
	"reflect"
	"testing"
)

func TestParseBashWriteTargets(t *testing.T) {
	tests := []struct {
		name    string
		command string
		want    []string
	}{
		{"echo redirect", "echo hello > src/a.go", []string{"src/a.go"}},
		{"append redirect", "echo hi >> docs/b.md", []string{"docs/b.md"}},
		{"redirect to devnull filtered", "cat x > /dev/null", nil},
		{"sed -i quoted script", "sed -i 's/a/b/' src/main.go", []string{"src/main.go"}},
		{"sed -i bare script", "sed -i s/a/b/ src/main.go", []string{"src/main.go"}},
		{"tee", "tee out.txt < in.txt", []string{"out.txt"}},
		{"touch", "touch docs/new.md", []string{"docs/new.md"}},
		{"cp dst", "cp src/a.go src/b.go", []string{"src/b.go"}},
		{"mv dst", "mv a.txt b.txt", []string{"b.txt"}},
		{"no write", "go build -o bin/x .", nil},
		{"pipeline no write", "echo x | grep y", nil},
		{"multi redirect", "echo a > x.txt; echo b > y.txt", []string{"x.txt", "y.txt"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := parseBashWriteTargets(tt.command)
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("parseBashWriteTargets(%q) = %v, want %v", tt.command, got, tt.want)
			}
		})
	}
}

func TestIsSystemPath(t *testing.T) {
	for _, p := range []string{"/dev/null", "/dev/stdout", "/proc/self", "/tmp/x", "NUL"} {
		if !isSystemPath(p) {
			t.Errorf("isSystemPath(%q) should be true", p)
		}
	}
	for _, p := range []string{"src/a.go", "docs/b.md", "go.mod"} {
		if isSystemPath(p) {
			t.Errorf("isSystemPath(%q) should be false", p)
		}
	}
}

func TestRunCheckBashWriteNoTaskBlocked(t *testing.T) {
	chdirTemp(t)
	// No task declared → bash write to a project file must be blocked.
	withStdin(t, `{"tool_name":"Bash","tool_input":{"command":"echo x > src/a.go"}}`, func() {
		code := runWithExitCapture(t, func() { runCheckBashWrite(nil) })
		if code != 2 {
			t.Errorf("bash write with no task should block (exit 2), got %d", code)
		}
	})
}

func TestRunCheckBashWriteSystemPathAllowed(t *testing.T) {
	chdirTemp(t)
	// Redirect to /dev/null is not a project write → allow.
	withStdin(t, `{"tool_name":"Bash","tool_input":{"command":"echo x > /dev/null"}}`, func() {
		code := runWithExitCapture(t, func() { runCheckBashWrite(nil) })
		if code != 0 {
			t.Errorf("system-path redirect should allow (exit 0), got %d", code)
		}
	})
}

func TestRunCheckBashWriteTaskScoped(t *testing.T) {
	dir := chdirTemp(t)
	writeRelFile(t, dir, "docs/tasks/TASKS.md", "# 任务清单\n\n| T-101 | init | P0 | — | 🔄 |\n")
	writeRelFile(t, dir, "docs/tasks/T-101.md", "# T-101\n**允许修改文件**：src/\n\n## 验收标准\n- [x] AC1\n")
	runBeginTask([]string{"T-101"})

	// Write inside allowed dir → allow.
	withStdin(t, `{"tool_name":"Bash","tool_input":{"command":"echo x > src/a.go"}}`, func() {
		code := runWithExitCapture(t, func() { runCheckBashWrite(nil) })
		if code != 0 {
			t.Errorf("bash write inside allowed dir should allow (exit 0), got %d", code)
		}
	})
	// Write outside allowed dir → block.
	withStdin(t, `{"tool_name":"Bash","tool_input":{"command":"echo x > deploy/nats.conf"}}`, func() {
		code := runWithExitCapture(t, func() { runCheckBashWrite(nil) })
		if code == 0 {
			t.Errorf("bash write outside allowed dir should block, got exit 0")
		}
	})
}

func TestRunCheckBashWriteNonBash(t *testing.T) {
	chdirTemp(t)
	withStdin(t, `{"tool_name":"Read","tool_input":{}}`, func() {
		code := runWithExitCapture(t, func() { runCheckBashWrite(nil) })
		if code != 0 {
			t.Errorf("non-Bash input should allow (exit 0), got %d", code)
		}
	})
}

func TestRunCheckScopeMalformedJSONFailClosed(t *testing.T) {
	chdirTemp(t)
	withStdin(t, `{not valid json`, func() {
		code := runWithExitCapture(t, func() { runCheckScope(nil) })
		if code != 2 {
			t.Errorf("malformed hook input should fail-closed (exit 2), got %d", code)
		}
	})
}

func TestRunCheckBashWriteMalformedJSONFailClosed(t *testing.T) {
	chdirTemp(t)
	withStdin(t, `{not valid json`, func() {
		code := runWithExitCapture(t, func() { runCheckBashWrite(nil) })
		if code != 2 {
			t.Errorf("malformed hook input should fail-closed (exit 2), got %d", code)
		}
	})
}

func TestParseBashWriteTargetsFlags(t *testing.T) {
	tests := []struct {
		name    string
		command string
		want    []string
	}{
		{"tee -a flag", "tee -a log.txt", []string{"log.txt"}},
		{"cp -r dst", "cp -r src dst", []string{"dst"}},
		{"mv -f dst", "mv -f a.txt b.txt", []string{"b.txt"}},
		{"touch -d date file", "touch -d 2020-01-01 src/new.go", []string{"src/new.go"}},
		{"git commit message with gt", `git commit -m "fix > thing"`, nil},
		{"redirect after quoted", `echo "hi" > src/a.go`, []string{"src/a.go"}},
		{"multi cp", "cp a b && cp c d", []string{"b", "d"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := parseBashWriteTargets(tt.command)
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("parseBashWriteTargets(%q) = %v, want %v", tt.command, got, tt.want)
			}
		})
	}
}

func TestReadHookInputToolInputNormalization(t *testing.T) {
	// Real Claude Code payload uses tool_input.
	withStdin(t, `{"tool_name":"Write","tool_input":{"file_path":"src/a.go"}}`, func() {
		in, err := readHookInput()
		if err != nil {
			t.Fatal(err)
		}
		if in.Input["file_path"] != "src/a.go" {
			t.Errorf("tool_input should normalize into Input, got %v", in.Input)
		}
	})
	// Legacy/test payload with input still works (compat).
	withStdin(t, `{"tool_name":"Write","input":{"file_path":"src/b.go"}}`, func() {
		in, err := readHookInput()
		if err != nil {
			t.Fatal(err)
		}
		if in.Input["file_path"] != "src/b.go" {
			t.Errorf("legacy input should still normalize, got %v", in.Input)
		}
	})
}
