package main

import (
	"testing"
)

func TestCheckPrerequisites(t *testing.T) {
	content := `| ID | Task | P | 依赖 | 状态 |
| T-001 | init | P0 | — | ✅ |
| T-002 | build | P0 | T-001 | ✅ |
| T-003 | test | P0 | T-002 | 🔄 |
| T-004 | deploy | P0 | T-003 | ⬜ |
| T-005 | release | P0 | T-004 | ⬜ |
| T-006 | standalone | P0 | — | ⬜ |`

	tests := []struct {
		name string
		id   string
		want bool
	}{
		{"no-deps", "T-006", true},
		{"deps-done", "T-003", true}, // T-002 ✅
		{"dep-pending", "T-005", false},
		{"self-not-in-table", "T-999", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ok, msg := checkPrerequisites(content, tt.id)
			if ok != tt.want {
				t.Errorf("checkPrerequisites(%s) = %v (%s), want %v", tt.id, ok, msg, tt.want)
			}
		})
	}
}

func TestCheckPrerequisitesDepBlocked(t *testing.T) {
	content := `| ID | Task | P | 依赖 | 状态 |
| T-001 | init | P0 | — | ❌ |
| T-002 | build | P0 | T-001 | ⬜ |`

	ok, msg := checkPrerequisites(content, "T-002")
	if ok {
		t.Error("dep blocked with ❌ should fail")
	}
	if msg == "" {
		t.Error("should report which dep failed")
	}
}

func TestCheckPrerequisitesDepInProgress(t *testing.T) {
	content := `| ID | Task | P | 依赖 | 状态 |
| T-001 | init | P0 | — | 🔄 |
| T-002 | build | P0 | T-001 | ⬜ |`

	if ok, _ := checkPrerequisites(content, "T-002"); ok {
		t.Error("dep in 🔄 should not satisfy prerequisite")
	}
}
