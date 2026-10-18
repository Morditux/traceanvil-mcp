package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/Morditux/traceanvil-mcp/internal/sbom"
)

func TestSBOMCommand(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := Run(context.Background(), []string{"sbom"}, nil, &stdout, &stderr); code != ExitSuccess {
		t.Fatalf("sbom exit = %d, stderr = %s", code, stderr.String())
	}
	var doc sbom.Document
	if err := json.Unmarshal(stdout.Bytes(), &doc); err != nil {
		t.Fatalf("SBOM JSON: %v\n%s", err, stdout.String())
	}
	if doc.SPDXVersion != "SPDX-2.3" || len(doc.Packages) == 0 {
		t.Fatalf("SBOM = %+v", doc)
	}
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	stdout.Reset()
	if code := Run(context.Background(), []string{"sbom", "--binary", exe, "--version", "v9.9.9"}, nil, &stdout, &stderr); code != ExitSuccess {
		t.Fatalf("sbom --binary exit = %d, stderr = %s", code, stderr.String())
	}
	var embedded sbom.Document
	if err := json.Unmarshal(stdout.Bytes(), &embedded); err != nil || embedded.Packages[0].VersionInfo != "v9.9.9" {
		t.Fatalf("embedded SBOM = %+v, %v", embedded, err)
	}
	path := filepath.Join(t.TempDir(), "sbom.json")
	stdout.Reset()
	if code := Run(context.Background(), []string{"sbom", "--output", path}, nil, &stdout, &stderr); code != ExitSuccess {
		t.Fatalf("sbom --output exit = %d, stderr = %s", code, stderr.String())
	}
	data, err := os.ReadFile(path)
	if err != nil || len(data) == 0 {
		t.Fatalf("SBOM file: %v", err)
	}
}
