package diagnostic

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
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
	// Length-prefix-free fields are separated by NUL and the recipe is fixed
	// and versioned, so concatenation cannot be confused with another recipe.
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
