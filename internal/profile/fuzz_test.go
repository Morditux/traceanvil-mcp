package profile

import (
	"bytes"
	"testing"
)

func FuzzParseCallgrind(f *testing.F) {
	f.Add([]byte("events: Ir\nfl=a.c\nfn=main\n1 10\n"))
	f.Fuzz(func(t *testing.T, data []byte) {
		_, _ = ParseCallgrind(bytes.NewReader(data))
	})
}

func FuzzParseMassif(f *testing.F) {
	f.Add([]byte("time_unit: i\nsnapshot=1\nmem_heap_B=1\n"))
	f.Fuzz(func(t *testing.T, data []byte) {
		_, _ = ParseMassif(bytes.NewReader(data))
	})
}
