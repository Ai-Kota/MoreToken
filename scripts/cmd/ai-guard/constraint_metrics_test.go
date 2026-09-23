package main

import (
	"os"
	"testing"
)

func TestCalculateMetrics(t *testing.T) {
	interceptions := []InterceptionRecord{
		{Rule: "R1", Tool: "check-read-before-write", Action: "blocked"},
		{Rule: "R1", Tool: "check-read-before-write", Action: "blocked"},
		{Rule: "X5", Tool: "check-scope", Action: "warned"},
		{Rule: "R2", Tool: "check-design-doc", Action: "allowed"},
	}
	compliance := []ComplianceRecord{
		{Result: "pass"},
		{Result: "fail"},
	}

	m := calculateMetrics(interceptions, compliance)

	if m.TotalInterceptions != 4 || m.Blocked != 2 || m.Warned != 1 || m.Allowed != 1 {
		t.Errorf("interception stats = total:%d blocked:%d warned:%d allowed:%d", m.TotalInterceptions, m.Blocked, m.Warned, m.Allowed)
	}
	if m.ByRule["R1"] != 2 || m.ByRule["X5"] != 1 {
		t.Errorf("ByRule = %v", m.ByRule)
	}
	if m.ByTool["check-scope"] != 1 {
		t.Errorf("ByTool = %v", m.ByTool)
	}
	if m.TotalVerifications != 2 || m.Passed != 1 || m.Failed != 1 {
		t.Errorf("verification stats = total:%d passed:%d failed:%d", m.TotalVerifications, m.Passed, m.Failed)
	}
}

func TestCalculateMetricsEmpty(t *testing.T) {
	m := calculateMetrics(nil, nil)
	if m.TotalInterceptions != 0 || m.TotalVerifications != 0 {
		t.Errorf("empty metrics should be zero, got %+v", m)
	}
	if m.ByRule == nil || m.ByTool == nil {
		t.Error("maps should be initialized")
	}
}

func TestGetRecentRecords(t *testing.T) {
	records := make([]InterceptionRecord, 5)
	for i := range records {
		records[i] = InterceptionRecord{Rule: string(rune('A' + i))}
	}

	if got := getRecentRecords(records, 3); len(got) != 3 {
		t.Errorf("getRecentRecords(5,3) = %d, want 3", len(got))
	}
	if got := getRecentRecords(records, 10); len(got) != 5 {
		t.Errorf("getRecentRecords(5,10) = %d, want 5", len(got))
	}
	if got := getRecentRecords(nil, 3); len(got) != 0 {
		t.Errorf("getRecentRecords(nil,3) = %d, want 0", len(got))
	}
}

func TestSortedMapKeys(t *testing.T) {
	m := map[string]int{"b": 2, "a": 1, "c": 3}
	keys := sortedMapKeys(m)
	if len(keys) != 3 || keys[0] != "a" || keys[1] != "b" || keys[2] != "c" {
		t.Errorf("sortedMapKeys = %v", keys)
	}
	if got := sortedMapKeys(nil); len(got) != 0 {
		t.Errorf("sortedMapKeys(nil) = %v, want empty", got)
	}
}

func TestLoadSaveInterceptions(t *testing.T) {
	tmpDir := t.TempDir()
	origWd, _ := os.Getwd()
	os.Chdir(tmpDir)
	defer os.Chdir(origWd)

	saveInterceptions([]InterceptionRecord{{Rule: "R1", Tool: "check-scope", Action: "blocked"}})
	records := loadInterceptions()
	if len(records) != 1 || records[0].Rule != "R1" {
		t.Errorf("roundtrip failed: %+v", records)
	}
}

func TestLoadInterceptionsMissing(t *testing.T) {
	tmpDir := t.TempDir()
	origWd, _ := os.Getwd()
	os.Chdir(tmpDir)
	defer os.Chdir(origWd)

	if records := loadInterceptions(); len(records) != 0 {
		t.Errorf("missing file should return empty, got %+v", records)
	}
}

func TestLoadComplianceMissing(t *testing.T) {
	tmpDir := t.TempDir()
	origWd, _ := os.Getwd()
	os.Chdir(tmpDir)
	defer os.Chdir(origWd)

	if records := loadCompliance(); len(records) != 0 {
		t.Errorf("missing compliance file should return empty, got %+v", records)
	}
}

func TestGetMetricsTaskID(t *testing.T) {
	tmpDir := t.TempDir()
	origWd, _ := os.Getwd()
	os.Chdir(tmpDir)
	defer os.Chdir(origWd)

	// No tracker file → empty.
	if id := getMetricsTaskID(); id != "" {
		t.Errorf("no tracker = %q, want empty", id)
	}

	// With tracker file → reads task_id.
	writeRelFile(t, tmpDir, ".claude/read_tracker.json", `{"task_id":"T-201","read_files":[]}`)
	if id := getMetricsTaskID(); id != "T-201" {
		t.Errorf("tracker task = %q, want T-201", id)
	}
}
