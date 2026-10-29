package main

import (
	"bytes"
	"encoding/binary"
	"encoding/hex"
	"os"
	"strings"
	"testing"
)

// snappyLiterals encodes b as a Snappy block of literal runs only: valid
// input for the decoder, with none of the compression.
func snappyLiterals(b []byte) []byte {
	out := binary.AppendUvarint(nil, uint64(len(b)))
	for len(b) > 0 {
		n := min(len(b), 1<<16)
		out = append(out, 62<<2, byte(n-1), byte((n-1)>>8), byte((n-1)>>16))
		out = append(out, b[:n]...)
		b = b[n:]
	}
	return out
}

// The committed blob is what GitHub's attestations API pointed at for cli/cli's
// v2.96.0 checksums (captured 2026-10-29: the API answered with a bundle_url,
// no inline bundle). It decodes to a bundle that verifies as GitHub's real
// release attestation: refused for the app only because it is cli/cli's.
func TestSnappyDecodesGitHubsAttestationBlob(t *testing.T) {
	blob, err := os.ReadFile("testdata/sigstore/release_bundle.json.sn")
	if err != nil {
		t.Fatal(err)
	}
	bundleJSON, err := snappyDecode(blob, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	_, digestHex, _ := loadSigstoreFixtures(t)
	digest, _ := hex.DecodeString(digestHex)
	_, err = verifyAppAttestation(bundleJSON, digest, "v2.96.0", embeddedAnchors(t))
	if err == nil || !strings.Contains(err.Error(), "is for cli/cli v2.96.0") {
		t.Fatalf("error = %v, want the decoded bundle verified and refused by its predicate", err)
	}
}

func TestSnappyDecode(t *testing.T) {
	for _, n := range []int{0, 1, 59, 60, 61, 300, 1 << 16, 1<<16 + 7} {
		in := bytes.Repeat([]byte("abcdefg"), n/7+1)[:n]
		got, err := snappyDecode(snappyLiterals(in), 1<<20)
		if err != nil || !bytes.Equal(got, in) {
			t.Errorf("%d bytes: %v", n, err)
		}
	}
	// "abcabcabc": three literals, then an overlapping copy (offset 3, length 6).
	copied := []byte{9, 2 << 2, 'a', 'b', 'c', 1 | (6-4)<<2, 3}
	if got, err := snappyDecode(copied, 64); err != nil || string(got) != "abcabcabc" {
		t.Errorf("overlapping copy = %q, %v", got, err)
	}

	for name, bad := range map[string][]byte{
		"empty":                {},
		"length over limit":    binary.AppendUvarint(nil, 1<<30),
		"truncated literal":    {5, 4 << 2, 'a'},
		"copy before output":   {4, 1, 1},
		"copy past the start":  {6, 0, 'a', 1, 2},
		"shorter than claimed": {3, 0, 'a'},
		"longer than claimed":  {1, 1 << 2, 'a', 'b'},
	} {
		if got, err := snappyDecode(bad, 64); err == nil {
			t.Errorf("%s: decoded %q", name, got)
		}
	}
}
