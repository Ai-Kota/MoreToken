package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestFindSrcDir(t *testing.T) {
	// Create: tmp/go.mod + tmp/sub → findSrcDir(sub, "") == tmp
	parent := t.TempDir()
	writeRelFile(t, parent, "go.mod", "module demo\n")
	sub := filepath.Join(parent, "sub")
	if err := os.MkdirAll(sub, 0755); err != nil {
		t.Fatal(err)
	}

	if got := findSrcDir(sub, ""); got != parent {
		t.Errorf("findSrcDir(%q) = %q, want %q", sub, got, parent)
	}
}

func TestFindSrcDirNone(t *testing.T) {
	// A dir tree without go.mod, bounded at its own root → empty.
	// stopAt=parent caps the upward walk, so a stray go.mod in an unrelated
	// ancestor (e.g. C:\Users\…\Temp\go.mod on this machine) is never returned.
	parent := t.TempDir()
	if got := findSrcDir(parent, parent); got != "" {
		t.Errorf("findSrcDir bounded at root without go.mod = %q, want empty", got)
	}
}

func TestFindSrcDirStopAtBounds(t *testing.T) {
	// go.mod exists ABOVE the stopAt boundary → must not be found.
	outer := t.TempDir()
	writeRelFile(t, outer, "go.mod", "module demo\n")
	inner := filepath.Join(outer, "inner")
	if err := os.MkdirAll(inner, 0755); err != nil {
		t.Fatal(err)
	}
	// stopAt=inner: search confined to inner; outer's go.mod is out of bounds.
	if got := findSrcDir(inner, inner); got != "" {
		t.Errorf("findSrcDir stopAt=inner found ancestor go.mod = %q, want empty", got)
	}
}

func TestPrintUsage(t *testing.T) {
	// Should not panic; smoke test the help text.
	printUsage()
}

func TestVersionConstant(t *testing.T) {
	if Version == "" {
		t.Error("Version should not be empty")
	}
}
