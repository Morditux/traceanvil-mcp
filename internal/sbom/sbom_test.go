package sbom

import (
	"runtime/debug"
	"strings"
	"testing"
	"time"
)

func TestGenerateFromBuildInfo(t *testing.T) {
	info := &debug.BuildInfo{
		GoVersion: "go1.25.0",
		Main:      debug.Module{Path: "example.com/server", Version: "v1.0.0"},
		Deps: []*debug.Module{
			{Path: "github.com/example/dep", Version: "v1.2.3", Sum: "h1:AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA="},
			{Path: "github.com/example/replaced", Version: "v1.0.0", Replace: &debug.Module{Path: "../local", Version: "v1.0.1"}},
			nil,
		},
	}
	now := time.Date(2026, 10, 18, 12, 0, 0, 0, time.UTC)
	doc, err := Generate(info, "server", "v1.0.0", now)
	if err != nil {
		t.Fatal(err)
	}
	if doc.SPDXVersion != "SPDX-2.3" || len(doc.Packages) != 4 || len(doc.Relationships) != 4 {
		t.Fatalf("document = %+v", doc)
	}
	if doc.Packages[0].Name != "example.com/server" {
		t.Fatalf("main package = %+v", doc.Packages[0])
	}
	foundSum := false
	for _, pkg := range doc.Packages {
		for _, ref := range pkg.ExternalRefs {
			if ref.Type == "go-module-sum" && strings.HasPrefix(ref.Locator, "h1:") {
				foundSum = true
			}
		}
	}
	if !foundSum {
		t.Fatal("dependency Go module sum reference missing")
	}
	if !strings.Contains(doc.DocumentNamespace, "server/v1.0.0") {
		t.Fatalf("namespace = %q", doc.DocumentNamespace)
	}
	if doc.CreationInfo.Created != "2026-10-18T12:00:00Z" {
		t.Fatalf("created = %q", doc.CreationInfo.Created)
	}
}

func TestGenerateRejectsNil(t *testing.T) {
	if _, err := Generate(nil, "x", "v1", time.Now()); err == nil {
		t.Fatal("expected nil build-info error")
	}
}

func TestPackageIDCollisionsAreNotDropped(t *testing.T) {
	used := map[string]bool{}
	a := uniquePackageID("github.com/a_b/c", used)
	b := uniquePackageID("github.com/a-b/c", used)
	if a == b {
		t.Fatalf("colliding dependencies share SPDXID %q", a)
	}
	if strings.ContainsAny(a+b, "~_@ /") {
		t.Fatalf("invalid SPDX IDs: %q %q", a, b)
	}
}
