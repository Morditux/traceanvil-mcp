package valgrind

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

func BenchmarkParseMemcheckFixture(b *testing.B) {
	data, err := os.ReadFile(filepath.Join("..", "..", "testdata", "fixtures", "valgrind", "invalid_read.xml"))
	if err != nil {
		b.Fatal(err)
	}
	b.SetBytes(int64(len(data)))
	b.ResetTimer()
	for range b.N {
		if _, err := Parse(bytes.NewReader(data), ParseOptions{}); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkParseLargeMemcheckDocument(b *testing.B) {
	data, err := os.ReadFile(filepath.Join("..", "..", "testdata", "fixtures", "valgrind", "invalid_read.xml"))
	if err != nil {
		b.Fatal(err)
	}
	start := bytes.Index(data, []byte("<error>"))
	end := bytes.Index(data, []byte("</error>"))
	if start < 0 || end < start {
		b.Fatal("fixture has no error block")
	}
	block := data[start : end+len("</error>")]
	var doc bytes.Buffer
	doc.Write(data[:start])
	for i := 0; i < 2000; i++ {
		doc.Write(block)
	}
	doc.Write(data[end+len("</error>"):])
	payload := doc.Bytes()
	b.SetBytes(int64(len(payload)))
	b.ResetTimer()
	for range b.N {
		if _, err := Parse(bytes.NewReader(payload), ParseOptions{}); err != nil {
			b.Fatal(err)
		}
	}
}
