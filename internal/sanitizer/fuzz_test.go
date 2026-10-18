package sanitizer

import (
	"bytes"
	"testing"
)

func FuzzParseSanitizer(f *testing.F) {
	f.Add([]byte("==1==ERROR: AddressSanitizer: heap-buffer-overflow\n"))
	f.Fuzz(func(t *testing.T, data []byte) {
		_, _ = Parse(ToolASan, bytes.NewReader(data), ParseOptions{})
	})
}
