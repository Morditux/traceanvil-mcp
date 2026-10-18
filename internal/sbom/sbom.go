// Package sbom emits a minimal SPDX 2.3 software bill of materials from the
// Go build information embedded in the server binary. It performs no network
// access and does not invent licences.
package sbom

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/url"
	"runtime/debug"
	"strings"
	"time"
)

// Document is a minimal SPDX 2.3 document.
type Document struct {
	SPDXVersion       string         `json:"spdxVersion"`
	DataLicense       string         `json:"dataLicense"`
	SPDXID            string         `json:"SPDXID"`
	Name              string         `json:"name"`
	DocumentNamespace string         `json:"documentNamespace"`
	CreationInfo      CreationInfo   `json:"creationInfo"`
	Packages          []Package      `json:"packages"`
	Relationships     []Relationship `json:"relationships"`
}

// CreationInfo identifies the generating tool and time.
type CreationInfo struct {
	Created  string   `json:"created"`
	Creators []string `json:"creators"`
}

// Package is one SPDX package entry.
type Package struct {
	SPDXID           string        `json:"SPDXID"`
	Name             string        `json:"name"`
	VersionInfo      string        `json:"versionInfo,omitempty"`
	DownloadLocation string        `json:"downloadLocation"`
	FilesAnalyzed    bool          `json:"filesAnalyzed"`
	LicenseConcluded string        `json:"licenseConcluded"`
	LicenseDeclared  string        `json:"licenseDeclared"`
	CopyrightText    string        `json:"copyrightText"`
	ExternalRefs     []ExternalRef `json:"externalRefs,omitempty"`
}

// ExternalRef is an SPDX package reference.
type ExternalRef struct {
	Category string `json:"referenceCategory"`
	Type     string `json:"referenceType"`
	Locator  string `json:"referenceLocator"`
}

// Relationship links the document to its described packages.
type Relationship struct {
	SPDXElementID      string `json:"spdxElementId"`
	RelationshipType   string `json:"relationshipType"`
	RelatedSPDXElement string `json:"relatedSpdxElement"`
}

// Generate builds an SPDX document from Go build information. documentName is
// the artifact identity (the release script includes the architecture), while
// the main package identity always comes from info.Main.Path. Dependencies are
// read from the embedded module graph; no files are read besides the in-memory
// build info.
func Generate(info *debug.BuildInfo, documentName, version string, now time.Time) (Document, error) {
	if info == nil {
		return Document{}, fmt.Errorf("sbom: nil Go build info")
	}
	if version == "" {
		version = "unknown"
	}
	mainPath := "mcp-native-diagnostics"
	if info.Main.Path != "" {
		mainPath = info.Main.Path
	}
	if documentName == "" {
		documentName = mainPath
	}
	if now.IsZero() {
		now = time.Now().UTC()
	}
	used := make(map[string]bool)
	mainID := uniquePackageID(mainPath, used)
	doc := Document{
		SPDXVersion: "SPDX-2.3", DataLicense: "CC0-1.0", SPDXID: "SPDXRef-DOCUMENT",
		Name:              documentName + "-" + version,
		DocumentNamespace: "https://traceanvil.local/spdx/" + documentName + "/" + version,
		CreationInfo:      CreationInfo{Created: now.UTC().Format(time.RFC3339), Creators: []string{"Tool: mcp-native-diagnostics-sbom"}},
		Packages: []Package{{
			SPDXID: mainID, Name: mainPath, VersionInfo: version,
			DownloadLocation: "NOASSERTION", FilesAnalyzed: false,
			LicenseConcluded: "NOASSERTION", LicenseDeclared: "NOASSERTION",
			CopyrightText: "NOASSERTION",
			ExternalRefs:  []ExternalRef{{Category: "PACKAGE-MANAGER", Type: "purl", Locator: golangPURL(mainPath, version)}},
		}},
		Relationships: []Relationship{{
			SPDXElementID: "SPDXRef-DOCUMENT", RelationshipType: "DESCRIBES", RelatedSPDXElement: mainID,
		}},
	}
	if info.GoVersion != "" {
		goVersion := strings.TrimPrefix(info.GoVersion, "go")
		goID := uniquePackageID("go", used)
		doc.Packages = append(doc.Packages, Package{
			SPDXID: goID, Name: "go", VersionInfo: goVersion,
			DownloadLocation: "NOASSERTION", FilesAnalyzed: false,
			LicenseConcluded: "NOASSERTION", LicenseDeclared: "NOASSERTION",
			CopyrightText: "NOASSERTION",
			ExternalRefs:  []ExternalRef{{Category: "PACKAGE-MANAGER", Type: "purl", Locator: golangPURL("stdlib", goVersion)}},
		})
		doc.Relationships = append(doc.Relationships, Relationship{
			SPDXElementID: goID, RelationshipType: "DEPENDENCY_OF", RelatedSPDXElement: mainID,
		})
	}
	for _, dep := range info.Deps {
		if dep == nil || dep.Path == "" {
			continue
		}
		depVersion := dep.Version
		replace := ""
		if dep.Replace != nil && dep.Replace.Path != "" {
			replace = dep.Replace.Path
			if dep.Replace.Version != "" {
				replace += "@" + dep.Replace.Version
			}
		}
		id := uniquePackageID(dep.Path, used)
		pkg := Package{
			SPDXID: id, Name: dep.Path, VersionInfo: depVersion,
			DownloadLocation: "NOASSERTION", FilesAnalyzed: false,
			LicenseConcluded: "NOASSERTION", LicenseDeclared: "NOASSERTION",
			CopyrightText: "NOASSERTION",
			ExternalRefs:  []ExternalRef{{Category: "PACKAGE-MANAGER", Type: "purl", Locator: golangPURL(dep.Path, depVersion)}},
		}
		if dep.Sum != "" {
			pkg.ExternalRefs = append(pkg.ExternalRefs,
				ExternalRef{Category: "OTHER", Type: "go-module-sum", Locator: dep.Sum})
		}
		if replace != "" {
			pkg.ExternalRefs = append(pkg.ExternalRefs,
				ExternalRef{Category: "OTHER", Type: "go-replace", Locator: replace})
		}
		doc.Packages = append(doc.Packages, pkg)
		doc.Relationships = append(doc.Relationships, Relationship{
			SPDXElementID: id, RelationshipType: "DEPENDENCY_OF", RelatedSPDXElement: mainID,
		})
	}
	return doc, nil
}

func golangPURL(path, version string) string {
	purl := "pkg:golang/" + strings.ToLower(path)
	if version != "" {
		purl += "@" + url.PathEscape(version)
	}
	return purl
}

func uniquePackageID(value string, used map[string]bool) string {
	base := packageID(value)
	if !used[base] {
		used[base] = true
		return base
	}
	hash := sha256.Sum256([]byte(value))
	candidate := base + "-" + hex.EncodeToString(hash[:4])
	if !used[candidate] {
		used[candidate] = true
		return candidate
	}
	for index := 2; ; index++ {
		candidate = fmt.Sprintf("%s-%d", base, index)
		if !used[candidate] {
			used[candidate] = true
			return candidate
		}
	}
}

func packageID(value string) string {
	var b strings.Builder
	for _, r := range value {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '.', r == '-':
			b.WriteRune(r)
		default:
			b.WriteByte('-')
		}
	}
	id := strings.Trim(b.String(), "-")
	if id == "" {
		id = "unknown"
	}
	return "SPDXRef-Package-" + id
}
