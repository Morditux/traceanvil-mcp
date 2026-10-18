package config

import (
	"bytes"
	"testing"
)

func FuzzParseConfig(f *testing.F) {
	f.Add([]byte(`{"schema_version":1,"workspace_roots":["/tmp/ws"],"report_dir":"/tmp/out"}`))
	f.Fuzz(func(t *testing.T, data []byte) {
		_, _ = Parse(bytes.NewReader(data))
	})
}
