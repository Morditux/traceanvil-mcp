package debugger

import (
	"io"
	"os"
	"path/filepath"
	"testing"
)

func TestGDBProcessReadsEverythingBeforeWaitCloses(t *testing.T) {
	home := t.TempDir()
	script := filepath.Join(home, "fake-gdb")
	payload := `#!/bin/sh
i=0
while [ $i -lt 10000 ]; do
  printf '0123456789012345678901234567890123456789'
  i=$((i+1))
done
exit 0
`
	if err := os.WriteFile(script, []byte(payload), 0o700); err != nil {
		t.Fatal(err)
	}

	process, err := startDebugProcess(script, nil, home, home)
	if err != nil {
		t.Fatal(err)
	}
	data, readErr := io.ReadAll(process)
	closeErr := process.Close()
	waitErr := process.Wait(0)
	if readErr != nil {
		t.Fatalf("ReadAll() error = %v", readErr)
	}
	if closeErr != nil {
		t.Errorf("Close() error = %v", closeErr)
	}
	if waitErr != nil {
		t.Logf("process wait error (fake script exits 0): %v", waitErr)
	}
	const want = 10000 * 40
	if len(data) != want {
		t.Errorf("read %d bytes, want %d (stdout pipe was closed by Wait?)", len(data), want)
	}
}
