package diagnostic

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
)

// maxFingerprintFrames bounds how many non-runtime frames contribute to a
// fingerprint. Deeper frames are usually allocator or runtime noise.
const maxFingerprintFrames = 16

// FingerprintV1 returns a stable digest for a tool-reported diagnostic.
//
// The recipe is origin + kind + selected non-runtime frames. Addresses, PIDs,
// TIDs, timestamps and run identifiers are deliberately excluded. Inlining,
// missing symbols and line movement can still change a fingerprint; callers
// must treat it as a versioned heuristic, never as proof of identity.
func FingerprintV1(origin, kind string, stack []Frame) string {
	hash := sha256.New()
	// v1 uses NUL-separated fields under a fixed version marker. It is kept for
	// persisted reports; v2 uses explicit length prefixes.
	fmt.Fprintf(hash, "v1\x00%s\x00%s\x00", origin, kind)

	selected := 0
	for _, frame := range stack {
		if frame.Runtime {
			continue
		}
		if frame.Function == "" && frame.Path == "" {
			continue
		}
		fmt.Fprintf(hash, "%s\x00%s\x00%d\x00", frame.Function, frame.Path, frame.Line)
		selected++
		if selected >= maxFingerprintFrames {
			break
		}
	}
	if selected == 0 {
		hash.Write([]byte("no-stack\x00"))
	}
	return "sha256:" + hex.EncodeToString(hash.Sum(nil))
}

// FingerprintV2 returns a versioned digest for a tool-reported diagnostic.
//
// The v2 recipe length-prefixes every field and includes the origin, the kind
// and up to 16 non-runtime frames. It deliberately excludes source lines:
// line movement in an otherwise identical stack must not create a new identity.
// Function names and normalized paths are included because they are the stable
// parts of a symbolized stack. Inlining, missing symbols and path remapping can
// still change the digest; callers must treat it as a versioned heuristic.
func FingerprintV2(origin, kind string, stack []Frame) string {
	hash := sha256.New()
	writeFingerprintField(hash, "v2")
	writeFingerprintField(hash, strings.TrimSpace(origin))
	writeFingerprintField(hash, strings.TrimSpace(kind))

	selected := 0
	for _, frame := range stack {
		if frame.Runtime {
			continue
		}
		function := strings.TrimSpace(frame.Function)
		path := strings.TrimSpace(frame.Path)
		if function == "" && path == "" {
			continue
		}
		writeFingerprintField(hash, function)
		writeFingerprintField(hash, path)
		selected++
		if selected >= maxFingerprintFrames {
			break
		}
	}
	if selected == 0 {
		writeFingerprintField(hash, "no-stack")
	}
	return "sha256:" + hex.EncodeToString(hash.Sum(nil))
}

func writeFingerprintField(hash interface{ Write([]byte) (int, error) }, value string) {
	// The length prefix makes the recipe unambiguous even when a function or
	// path contains the separator used by another field.
	fmt.Fprintf(hash, "%d:", len(value))
	_, _ = hash.Write([]byte(value))
}
