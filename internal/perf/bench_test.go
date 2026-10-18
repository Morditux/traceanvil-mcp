package perf

import (
	"fmt"
	"strings"
	"testing"
)

func BenchmarkParseStatLarge(b *testing.B) {
	var doc strings.Builder
	for i := 0; i < 100000; i++ {
		fmt.Fprintf(&doc, `{"event":"cycles","counter-value":"%d"}`+"\n", i)
	}
	data := doc.String()
	b.SetBytes(int64(len(data)))
	b.ResetTimer()
	for range b.N {
		if _, err := ParseStat(strings.NewReader(data)); err != nil {
			b.Fatal(err)
		}
	}
}
