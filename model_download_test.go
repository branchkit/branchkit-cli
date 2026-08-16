package main

import (
	"os"
	"path/filepath"
	"testing"
)

// The invariant these three tests used to guard — "every WhisperKit model must
// bring its tokenizer, or the first dictation fails network-denied" — is now
// structural rather than tested here. The model declaration lists the
// tokenizer as a part AND names it in `requires`, so a model missing it fails
// the completeness gate at provisioning time instead of at first dictation.
// See notes/DESIGN_PLUGIN_MODEL_DECLARATION.md in branchkit/app.

func TestVerifySHA256(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "blob")
	if err := os.WriteFile(path, []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}
	// echo -n hello | shasum -a 256
	const want = "2cf24dba5fb0a30e26e83b2ac5b9e29e1b161e5c1fa7425e73043362938b9824"

	if err := verifySHA256(path, want); err != nil {
		t.Fatalf("matching hash should pass: %v", err)
	}
	if err := verifySHA256(path, "2CF24DBA5FB0A30E26E83B2AC5B9E29E1B161E5C1FA7425E73043362938B9824"); err != nil {
		t.Fatalf("hash compare should be case-insensitive: %v", err)
	}
	if err := verifySHA256(path, "deadbeef"); err == nil {
		t.Fatal("mismatched hash should fail")
	}
	if err := verifySHA256(filepath.Join(dir, "nope"), want); err == nil {
		t.Fatal("missing file should error")
	}
}
