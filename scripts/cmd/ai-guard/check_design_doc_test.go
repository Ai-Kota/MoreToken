package main

import (
	"os"
	"testing"
)

func TestCheckDesignDocExists(t *testing.T) {
	tmpDir := t.TempDir()
	origWd, _ := os.Getwd()
	os.Chdir(tmpDir)
	defer os.Chdir(origWd)

	// No docs → not found, expected path given.
	exists, expected := checkDesignDocExists("auth")
	if exists {
		t.Error("should not exist before doc is created")
	}
	if expected != "docs/architecture/modules/auth.md" {
		t.Errorf("expected path = %q", expected)
	}

	// Create the doc → found.
	writeRelFile(t, tmpDir, "docs/architecture/modules/auth.md", "# auth")
	exists, expected = checkDesignDocExists("auth")
	if !exists {
		t.Error("should exist after doc is created")
	}
	if expected != "docs/architecture/modules/auth.md" {
		t.Errorf("expected path = %q", expected)
	}
}

func TestCheckDesignDocExistsGlob(t *testing.T) {
	tmpDir := t.TempDir()
	origWd, _ := os.Getwd()
	os.Chdir(tmpDir)
	defer os.Chdir(origWd)

	// Suffixed doc (auth-core.md) should match module prefix "auth".
	writeRelFile(t, tmpDir, "docs/architecture/modules/auth-core.md", "# auth-core")
	exists, _ := checkDesignDocExists("auth")
	if !exists {
		t.Error("module doc with suffix should match via glob")
	}
}
