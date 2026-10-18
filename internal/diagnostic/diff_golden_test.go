package diagnostic

import (
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

var updateDiffGolden = flag.Bool("update-diff-golden", false, "update diagnostics diff golden files")

func TestDiffGolden(t *testing.T) {
	dir := filepath.Join("..", "..", "testdata", "golden", "diff")
	base := readGoldenReport(t, filepath.Join(dir, "base.json"))
	target := readGoldenReport(t, filepath.Join(dir, "target.json"))
	result, err := Compare(base, target)
	if err != nil {
		t.Fatalf("Compare() error = %v", err)
	}
	data, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "expected.json")
	if *updateDiffGolden {
		if err := os.WriteFile(path, append(data, '\n'), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read golden (run with -update-diff-golden): %v", err)
	}
	var wantValue, gotValue any
	if err := json.Unmarshal(want, &wantValue); err != nil {
		t.Fatalf("decode golden: %v", err)
	}
	if err := json.Unmarshal(data, &gotValue); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(gotValue, wantValue) {
		t.Fatalf("diff result does not match %s\n%s", path, data)
	}
}

func readGoldenReport(t *testing.T, path string) Report {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var report Report
	if err := json.Unmarshal(data, &report); err != nil {
		t.Fatalf("decode %s: %v", path, err)
	}
	return report
}
