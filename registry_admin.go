package main

// Registry maintainer commands for the counter-signature (the fork moat):
//
//   registry keygen         generate the one registry signing keypair
//   registry sign ...        counter-sign a plugin's catalog listing
//
// The private key is the whole moat, so custody is deliberately flexible and
// NOT tied to any provider. `keygen` runs on your machine; you paste the
// private key into whatever secret store your CI uses (GitHub Actions
// secrets, GitLab CI variables, …) and hand the public key to the app/CLI to
// embed. Because only the public key is ever distributed, moving the private
// key to a different vault — or to a KMS/HSM — later changes nothing that
// ships and needs no plugin re-signing.
//
// Signing goes through the `registrySigner` seam so the key SOURCE is
// swappable: today a local Ed25519 key (from a file or env var); a KMS/HSM
// backend that signs without exposing the key can implement the same
// interface later without touching callers or the verification path.

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"os"
)

// registrySigner produces a registry counter-signature. Local key today;
// KMS/HSM later via the same interface.
type registrySigner interface {
	signCounterSig(manifestHash, attestationDigest string) (string, error)
}

// localKeySigner holds an Ed25519 private key in-process (file/env backend).
type localKeySigner struct{ priv ed25519.PrivateKey }

func (s localKeySigner) signCounterSig(manifestHash, attestationDigest string) (string, error) {
	if len(s.priv) != ed25519.PrivateKeySize {
		return "", fmt.Errorf("registry private key is not a valid Ed25519 key")
	}
	return signRegistryCounterSig(s.priv, manifestHash, attestationDigest), nil
}

// loadLocalSigner reads the private key from --key <file> if keyPath is set,
// else the BRANCHKIT_REGISTRY_KEY env var (how CI passes a secret). Both carry
// the base64 of the raw Ed25519 private key that `registry keygen` printed.
func loadLocalSigner(keyPath string) (registrySigner, error) {
	var b64 string
	switch {
	case keyPath != "":
		data, err := os.ReadFile(keyPath)
		if err != nil {
			return nil, fmt.Errorf("read key file: %w", err)
		}
		b64 = string(data)
	case os.Getenv("BRANCHKIT_REGISTRY_KEY") != "":
		b64 = os.Getenv("BRANCHKIT_REGISTRY_KEY")
	default:
		return nil, fmt.Errorf("no signing key: pass --key <file> or set BRANCHKIT_REGISTRY_KEY")
	}
	priv, err := parseRegistryPrivateKey(b64)
	if err != nil {
		return nil, err
	}
	return localKeySigner{priv: priv}, nil
}

func parseRegistryPrivateKey(b64 string) (ed25519.PrivateKey, error) {
	raw, err := base64.StdEncoding.DecodeString(trimField(b64))
	if err != nil {
		return nil, fmt.Errorf("registry private key is not valid base64: %w", err)
	}
	if len(raw) != ed25519.PrivateKeySize {
		return nil, fmt.Errorf("registry private key has wrong length %d (want %d)", len(raw), ed25519.PrivateKeySize)
	}
	return ed25519.PrivateKey(raw), nil
}

// generateRegistryKeypair returns (privateB64, publicB64) for a fresh Ed25519
// keypair, in the base64 forms the sign/verify code consumes.
func generateRegistryKeypair() (string, string, error) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return "", "", err
	}
	return base64.StdEncoding.EncodeToString(priv),
		base64.StdEncoding.EncodeToString(pub), nil
}

// sha256HexFile returns the lowercase hex SHA-256 of a file's bytes — the
// digest form both the counter-sig payload and the install-time recompute use.
func sha256HexFile(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%x", sha256.Sum256(data)), nil
}

func trimField(s string) string {
	// Secrets often arrive with a trailing newline; strip surrounding space.
	for len(s) > 0 && (s[len(s)-1] == '\n' || s[len(s)-1] == '\r' || s[len(s)-1] == ' ' || s[len(s)-1] == '\t') {
		s = s[:len(s)-1]
	}
	for len(s) > 0 && (s[0] == ' ' || s[0] == '\t' || s[0] == '\n' || s[0] == '\r') {
		s = s[1:]
	}
	return s
}

func cmdRegistryKeygen(_ []string) {
	priv, pub, err := generateRegistryKeypair()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error generating keypair: %v\n", err)
		os.Exit(1)
	}
	fmt.Println("Registry signing keypair generated.")
	fmt.Println()
	fmt.Println("PRIVATE KEY — store as a CI secret (e.g. BRANCHKIT_REGISTRY_KEY).")
	fmt.Println("Never commit it, never share it. This key is the anti-clone moat.")
	fmt.Println()
	fmt.Println("  " + priv)
	fmt.Println()
	fmt.Println("PUBLIC KEY — safe to share; embed it in the app/CLI to verify signatures.")
	fmt.Println()
	fmt.Println("  " + pub)
	fmt.Println()
}

func cmdRegistrySign(args []string) {
	var manifestPath, attestationPath, keyPath string
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--manifest":
			i++
			manifestPath = argAt(args, i)
		case "--attestation":
			i++
			attestationPath = argAt(args, i)
		case "--key":
			i++
			keyPath = argAt(args, i)
		}
	}
	if manifestPath == "" || attestationPath == "" {
		fmt.Fprintln(os.Stderr, "Usage: branchkit-cli registry sign --manifest plugin.json --attestation artifact.sigstore.json [--key file]")
		fmt.Fprintln(os.Stderr, "  (or set BRANCHKIT_REGISTRY_KEY instead of --key)")
		os.Exit(1)
	}

	manifestHash, err := sha256HexFile(manifestPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error hashing manifest: %v\n", err)
		os.Exit(1)
	}
	attestationDigest, err := sha256HexFile(attestationPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error hashing attestation: %v\n", err)
		os.Exit(1)
	}
	signer, err := loadLocalSigner(keyPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
	sig, err := signer.signCounterSig(manifestHash, attestationDigest)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error signing: %v\n", err)
		os.Exit(1)
	}

	// Print the fields to record in the plugin's catalog.yaml entry.
	fmt.Printf("manifest_sha256:    %s\n", manifestHash)
	fmt.Printf("attestation_sha256: %s\n", attestationDigest)
	fmt.Printf("registry_signature: %s\n", sig)
}

func printRegistryUsage() {
	fmt.Println("Usage: branchkit-cli registry <command>")
	fmt.Println()
	fmt.Println("Maintainer commands for the registry counter-signature (fork moat).")
	fmt.Println()
	fmt.Println("Commands:")
	fmt.Println("  keygen                Generate the registry signing keypair (run once, locally)")
	fmt.Println("  sign --manifest plugin.json --attestation bundle.sigstore.json [--key file]")
	fmt.Println("        Counter-sign a plugin's catalog listing (needs the private key)")
}
