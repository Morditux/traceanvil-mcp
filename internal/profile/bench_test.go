package profile

import (
	"fmt"
	"strings"
	"testing"
)

func BenchmarkParseCallgrindLarge(b *testing.B) {
	var doc strings.Builder
	doc.WriteString("events: Ir\nsummary: 100000000\n")
	for fn := 0; fn < 2000; fn++ {
		fmt.Fprintf(&doc, "fl=file%d.c\nfn=function%d\n", fn, fn)
		for line := 0; line < 50; line++ {
			fmt.Fprintf(&doc, "%d %d\n", line+1, line+fn+1)
		}
	}
	data := doc.String()
	b.SetBytes(int64(len(data)))
	b.ResetTimer()
	for range b.N {
		if _, err := ParseCallgrind(strings.NewReader(data)); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkParseMassifLarge(b *testing.B) {
	var doc strings.Builder
	doc.WriteString("time_unit: i\n")
	for i := 0; i < 5000; i++ {
		fmt.Fprintf(&doc, "snapshot=%d\ntime=%d\nmem_heap_B=%d\nmem_heap_extra_B=8\nmem_stacks_B=0\nheap_tree=empty\n", i, i, i*1024)
	}
	data := doc.String()
	b.SetBytes(int64(len(data)))
	b.ResetTimer()
	for range b.N {
		if _, err := ParseMassif(strings.NewReader(data)); err != nil {
			b.Fatal(err)
		}
	}
}
