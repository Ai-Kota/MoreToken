package admission

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// T-049：准入表跨轮稳定性。
//
// 评测器每 30 分钟重写一次表；单轮低分（如 agnes coding 3/5）不得覆盖
// 窗口内已经过线的证据（4/5）。窗口外的旧证据必须淘汰，避免无限期掩盖退化。

func dimsJSON(coding, reasoning [2]int) string {
	var parts []string
	if coding[1] > 0 {
		parts = append(parts, fmt.Sprintf(`"coding":{"passed":%d,"total":%d}`, coding[0], coding[1]))
	}
	if reasoning[1] > 0 {
		parts = append(parts, fmt.Sprintf(`"reasoning":{"passed":%d,"total":%d}`, reasoning[0], reasoning[1]))
	}
	return "{" + strings.Join(parts, ",") + "}"
}

func rowJSON(model string, coding, reasoning [2]int, at time.Time) string {
	return fmt.Sprintf(
		`{"model":%q,"base":"https://apihub.agnes-ai.com","anthropic_ok":true,`+
			`"battery_verdict":"UNCERTAIN","checked_at":%q,"dims":%s}`,
		model, at.UTC().Format(time.RFC3339Nano), dimsJSON(coding, reasoning))
}

func writeRows(t *testing.T, p string, rows ...string) {
	t.Helper()
	body := "[" + strings.Join(rows, ",") + "]"
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatalf("写表失败: %v", err)
	}
}

func newMergeTable(p string, now *time.Time) *Table {
	return &Table{
		path: p, byNormal: map[string]rowState{}, minRatio: 0.8,
		now:            func() time.Time { return *now },
		evidenceWindow: defaultEvidenceWindow,
	}
}

func TestMerge_RetainsBetterCodingWithinWindow(t *testing.T) {
	base := time.Date(2026, 10, 25, 10, 0, 0, 0, time.UTC)
	now := base
	p := filepath.Join(t.TempDir(), "admitted.json")
	writeRows(t, p, rowJSON("m", [2]int{4, 5}, [2]int{5, 5}, base))
	tab := newMergeTable(p, &now)
	tab.reload()
	if !tab.Admitted("m", DimCoding) {
		t.Fatal("初始 4/5 应准入")
	}

	now = base.Add(time.Hour)
	writeRows(t, p, rowJSON("m", [2]int{3, 5}, [2]int{5, 5}, now))
	tab.reload()

	if !tab.Admitted("m", DimCoding) {
		t.Fatal("窗口内旧证据 4/5 被单轮 3/5 覆盖了 —— T-049 修复失效")
	}
	if got := tab.Rows()[0].Dims[DimCoding].Passed; got != 4 {
		t.Fatalf("保留的 coding = %d/5，want 4/5", got)
	}
}

func TestMerge_ExpiresStaleEvidence(t *testing.T) {
	base := time.Date(2026, 10, 25, 10, 0, 0, 0, time.UTC)
	now := base
	p := filepath.Join(t.TempDir(), "admitted.json")
	writeRows(t, p, rowJSON("m", [2]int{4, 5}, [2]int{5, 5}, base))
	tab := newMergeTable(p, &now)
	tab.reload()

	now = base.Add(4 * time.Hour) // 超过 3h 窗口
	writeRows(t, p, rowJSON("m", [2]int{3, 5}, [2]int{5, 5}, now))
	tab.reload()

	if tab.Admitted("m", DimCoding) {
		t.Fatal("窗口外的旧 4/5 必须淘汰，不能无限期掩盖退化")
	}
}

func TestMerge_PerDimension(t *testing.T) {
	base := time.Date(2026, 10, 25, 10, 0, 0, 0, time.UTC)
	now := base
	p := filepath.Join(t.TempDir(), "admitted.json")
	writeRows(t, p, rowJSON("m", [2]int{4, 5}, [2]int{3, 5}, base))
	tab := newMergeTable(p, &now)
	tab.reload()

	now = base.Add(time.Hour)
	writeRows(t, p, rowJSON("m", [2]int{3, 5}, [2]int{5, 5}, now))
	tab.reload()

	// coding 保留旧 4/5，reasoning 采用新 5/5 —— 按维度合并，不是整行二选一。
	if !tab.Admitted("m", DimCoding) {
		t.Error("coding 应保留窗口内的 4/5")
	}
	if !tab.Admitted("m", DimReasoning) {
		t.Error("reasoning 应采用新的 5/5")
	}
	row := tab.Rows()[0]
	if row.Dims[DimCoding].Passed != 4 || row.Dims[DimReasoning].Passed != 5 {
		t.Fatalf("合并后 dims = %+v，want coding 4/5 + reasoning 5/5", row.Dims)
	}
}

func TestMerge_KeepsUnmeasuredModelWithinWindow(t *testing.T) {
	base := time.Date(2026, 10, 25, 10, 0, 0, 0, time.UTC)
	now := base
	p := filepath.Join(t.TempDir(), "admitted.json")
	writeRows(t, p, rowJSON("m1", [2]int{5, 5}, [2]int{5, 5}, base))
	tab := newMergeTable(p, &now)
	tab.reload()

	now = base.Add(time.Hour)
	writeRows(t, p, rowJSON("m2", [2]int{5, 5}, [2]int{5, 5}, now))
	tab.reload()
	if !tab.Admitted("m1", DimCoding) {
		t.Fatal("本轮没测到的 m1 在窗口内应保留")
	}
	if !tab.Admitted("m2", DimCoding) {
		t.Fatal("m2 应准入")
	}

	now = base.Add(4 * time.Hour)
	writeRows(t, p, rowJSON("m2", [2]int{5, 5}, [2]int{5, 5}, now))
	tab.reload()
	if tab.Admitted("m1", DimCoding) {
		t.Fatal("m1 的旧证据已超过窗口，必须淘汰")
	}
}

func TestMerge_NewBetterWins(t *testing.T) {
	base := time.Date(2026, 10, 25, 10, 0, 0, 0, time.UTC)
	now := base
	p := filepath.Join(t.TempDir(), "admitted.json")
	writeRows(t, p, rowJSON("m", [2]int{3, 5}, [2]int{5, 5}, base))
	tab := newMergeTable(p, &now)
	tab.reload()
	if tab.Admitted("m", DimCoding) {
		t.Fatal("初始 3/5 不应准入")
	}

	now = base.Add(time.Hour)
	writeRows(t, p, rowJSON("m", [2]int{4, 5}, [2]int{5, 5}, now))
	tab.reload()
	if !tab.Admitted("m", DimCoding) {
		t.Fatal("新证据 4/5 应取代旧 3/5")
	}
}

func TestMerge_InvalidNewKeepsOld(t *testing.T) {
	base := time.Date(2026, 10, 25, 10, 0, 0, 0, time.UTC)
	now := base
	p := filepath.Join(t.TempDir(), "admitted.json")
	writeRows(t, p, rowJSON("m", [2]int{4, 5}, [2]int{5, 5}, base))
	tab := newMergeTable(p, &now)
	tab.reload()

	now = base.Add(time.Hour)
	writeRows(t, p, `{"model":"m","anthropic_ok":false,"battery_verdict":"","dims":{}}`)
	tab.reload()
	if !tab.Admitted("m", DimCoding) {
		t.Fatal("新表该行无效时应保留上一版有效证据（sync 的 valid-row 口径）")
	}
	if tab.Err() == "" {
		t.Error("无效新表应在 Err() 上可见")
	}
}

func TestMerge_EmptyFileKeepsOld(t *testing.T) {
	base := time.Date(2026, 10, 25, 10, 0, 0, 0, time.UTC)
	now := base
	p := filepath.Join(t.TempDir(), "admitted.json")
	writeRows(t, p, rowJSON("m", [2]int{4, 5}, [2]int{5, 5}, base))
	tab := newMergeTable(p, &now)
	tab.reload()

	now = base.Add(time.Hour)
	writeRows(t, p) // []
	tab.reload()
	if !tab.Admitted("m", DimCoding) {
		t.Fatal("空生成物不得清空在用的表")
	}
	if tab.Err() == "" {
		t.Error("空表应留下可见的 Err")
	}
}
