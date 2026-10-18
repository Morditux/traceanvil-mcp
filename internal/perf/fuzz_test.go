package perf

import (
	"bytes"
	"testing"
)

func FuzzParseStat(f *testing.F) {
	f.Add([]byte(`{"event":"cycles","counter-value":"1"}`))
	f.Fuzz(func(t *testing.T, data []byte) {
		_, _ = ParseStat(bytes.NewReader(data))
	})
}
