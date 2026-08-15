package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Every WhisperKit model needs a tokenizer repo, and the failure mode for a
// missing one is remote: the model downloads fine and the stage refuses to
// load at first dictation, network-denied (see provisionTokenizer).
func TestWhisperKitModelsDeclareATokenizerRepo(t *testing.T) {
	for ref, entry := range modelCatalog {
		if !strings.HasPrefix(ref, "whisperkit/") {
			continue
		}
		if entry.TokenizerRepo == "" {
			t.Errorf("%s: no TokenizerRepo — first dictation would fail under the stage sandbox", ref)
			continue
		}
		if !strings.HasPrefix(entry.TokenizerRepo, "openai/whisper-") {
			t.Errorf("%s: TokenizerRepo %q is not an openai/whisper-* repo", ref, entry.TokenizerRepo)
		}
	}
}

// Provisioning is skipped when a tokenizer is already on disk, so an existing
// install (or one seeded by an earlier live load) is not re-downloaded. This
// is the path `model download` takes on every already-present model.
func TestProvisionTokenizerSkipsWhenPresent(t *testing.T) {
	dest := t.TempDir()
	entry := modelEntry{TokenizerRepo: "openai/whisper-small.en"}
	dir := filepath.Join(dest, "models", "openai", "whisper-small.en")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "tokenizer.json"), []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	// No network is available to this test; reaching the download path would
	// error, so a nil return proves the skip.
	if err := provisionTokenizer("whisperkit/openai_whisper-small.en", entry, dest); err != nil {
		t.Fatalf("present tokenizer should short-circuit: %v", err)
	}
}

// A model with no TokenizerRepo (sherpa, or any future in-folder tokenizer)
// must not be dragged through the WhisperKit path.
func TestProvisionTokenizerIgnoresModelsWithoutARepo(t *testing.T) {
	if err := provisionTokenizer("sherpa/sherpa-offline-nemo", modelEntry{}, t.TempDir()); err != nil {
		t.Fatalf("model without a tokenizer repo should be a no-op: %v", err)
	}
}

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
