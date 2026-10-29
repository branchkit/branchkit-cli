package main

// A Sigstore instance made up for tests: its own Fulcio CA, which can issue a
// certificate with any SAN, OIDC issuer and source-repository extension, and
// sigstore-go's virtual timestamp authority. fakeSigstore.attest returns real
// bundle JSON, so a test drives the same parse-and-verify path as a bundle
// downloaded from a release.
//
// sigstore-go's own VirtualSigstore cannot stand in alone: its certificates
// carry only an email SAN and an issuer, and the identities under test here
// are a workflow URI with a SourceRepositoryURI.

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/asn1"
	"encoding/json"
	"fmt"
	"math/big"
	"net/url"
	"testing"
	"time"

	protobundle "github.com/sigstore/protobuf-specs/gen/pb-go/bundle/v1"
	protocommon "github.com/sigstore/protobuf-specs/gen/pb-go/common/v1"
	protodsse "github.com/sigstore/protobuf-specs/gen/pb-go/dsse"
	"github.com/sigstore/sigstore-go/pkg/root"
	"github.com/sigstore/sigstore-go/pkg/testing/ca"
	"google.golang.org/protobuf/encoding/protojson"
)

type fakeSigstore struct {
	*ca.VirtualSigstore // the timestamp authority, and empty logs
	fulcio              *root.FulcioCertificateAuthority
	intermediateKey     *ecdsa.PrivateKey
}

func newFakeSigstore(t *testing.T) *fakeSigstore {
	t.Helper()
	vs, err := ca.NewVirtualSigstore()
	if err != nil {
		t.Fatal(err)
	}
	rootCert, rootKey, err := ca.GenerateRootCa()
	if err != nil {
		t.Fatal(err)
	}
	intermediate, intermediateKey, err := ca.GenerateFulcioIntermediate(rootCert, rootKey)
	if err != nil {
		t.Fatal(err)
	}
	return &fakeSigstore{
		VirtualSigstore: vs,
		fulcio: &root.FulcioCertificateAuthority{
			Root:                rootCert,
			Intermediates:       []*x509.Certificate{intermediate},
			ValidityPeriodStart: time.Now().Add(-time.Hour),
			ValidityPeriodEnd:   time.Now().Add(time.Hour),
			URI:                 "https://fulcio.test.invalid",
		},
		intermediateKey: intermediateKey,
	}
}

func (f *fakeSigstore) FulcioCertificateAuthorities() []root.CertificateAuthority {
	return []root.CertificateAuthority{f.fulcio}
}

// anchor is this instance as a trust anchor. github says whether the CLI is
// to treat it as GitHub's own instance.
func (f *fakeSigstore) anchor(github bool) trustAnchor {
	return trustAnchor{material: f, github: github}
}

// fakeIdentity is what a certificate says about its holder. A SAN that
// parses as a URL with a scheme goes in as a URI SAN, anything else as an
// email address; Issuer and SourceRepo are left out when empty.
type fakeIdentity struct {
	SAN        string
	Issuer     string
	SourceRepo string
}

// Identities used across the tests.
var (
	emailIdentity = fakeIdentity{SAN: "mallory@example.com", Issuer: "https://accounts.google.com"}
	signerOnly    = fakeIdentity{SAN: releaseSignerSAN}
)

func actionsIdentity(repo, workflowRef string) fakeIdentity {
	return fakeIdentity{
		SAN:        "https://github.com/" + repo + "/.github/workflows/" + workflowRef,
		Issuer:     actionsIssuer,
		SourceRepo: "https://github.com/" + repo,
	}
}

// statement is an in-toto v1 statement over one subject.
func statement(subjectName, digestHex, predicateType string, predicate map[string]any) []byte {
	s, _ := json.Marshal(map[string]any{
		"_type":         "https://in-toto.io/Statement/v1",
		"subject":       []any{map[string]any{"name": subjectName, "digest": map[string]string{"sha256": digestHex}}},
		"predicateType": predicateType,
		"predicate":     predicate,
	})
	return s
}

// releasePredicate is the shape of GitHub's release predicate (the committed
// cli/cli and astral-sh/uv bundles carry it).
func releasePredicate(repo, tag string) map[string]any {
	return map[string]any{"repository": repo, "tag": tag, "purl": "pkg:github/" + repo + "@" + tag}
}

const releasePredicateType = "https://in-toto.io/attestation/release/v0.2"

// attest signs payload (an in-toto statement) as id and returns the bundle
// JSON: a v0.3 bundle holding the certificate, a DSSE envelope and an
// RFC 3161 timestamp, the shape GitHub's own instance publishes.
func (f *fakeSigstore) attest(t *testing.T, id fakeIdentity, payload []byte) []byte {
	t.Helper()
	leafKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	leaf := f.issue(t, id, leafKey)

	const payloadType = "application/vnd.in-toto+json"
	pae := fmt.Sprintf("DSSEv1 %d %s %d %s", len(payloadType), payloadType, len(payload), payload)
	digest := sha256.Sum256([]byte(pae))
	sig, err := ecdsa.SignASN1(rand.Reader, leafKey, digest[:])
	if err != nil {
		t.Fatal(err)
	}
	tsr, err := f.TimestampResponse(sig)
	if err != nil {
		t.Fatal(err)
	}

	pb := &protobundle.Bundle{
		MediaType: "application/vnd.dev.sigstore.bundle.v0.3+json",
		VerificationMaterial: &protobundle.VerificationMaterial{
			Content: &protobundle.VerificationMaterial_Certificate{
				Certificate: &protocommon.X509Certificate{RawBytes: leaf.Raw},
			},
			TimestampVerificationData: &protobundle.TimestampVerificationData{
				Rfc3161Timestamps: []*protocommon.RFC3161SignedTimestamp{{SignedTimestamp: tsr}},
			},
		},
		Content: &protobundle.Bundle_DsseEnvelope{DsseEnvelope: &protodsse.Envelope{
			Payload:     payload,
			PayloadType: payloadType,
			Signatures:  []*protodsse.Signature{{Sig: sig}},
		}},
	}
	out, err := protojson.Marshal(pb)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// issue is the Fulcio half: a short-lived code-signing certificate for key.
func (f *fakeSigstore) issue(t *testing.T, id fakeIdentity, key *ecdsa.PrivateKey) *x509.Certificate {
	t.Helper()
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(time.Now().UnixNano()),
		Subject:      pkix.Name{},
		NotBefore:    time.Now().Add(-time.Minute),
		NotAfter:     time.Now().Add(10 * time.Minute),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageCodeSigning},
	}
	if u, err := url.Parse(id.SAN); err == nil && u.Scheme != "" {
		tmpl.URIs = []*url.URL{u}
	} else {
		tmpl.EmailAddresses = []string{id.SAN}
	}
	ext := func(oid asn1.ObjectIdentifier, value string) {
		der, err := asn1.MarshalWithParams(value, "utf8")
		if err != nil {
			t.Fatal(err)
		}
		tmpl.ExtraExtensions = append(tmpl.ExtraExtensions, pkix.Extension{Id: oid, Value: der})
	}
	if id.Issuer != "" {
		ext(asn1.ObjectIdentifier{1, 3, 6, 1, 4, 1, 57264, 1, 8}, id.Issuer)
	}
	if id.SourceRepo != "" {
		ext(asn1.ObjectIdentifier{1, 3, 6, 1, 4, 1, 57264, 1, 12}, id.SourceRepo)
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, f.fulcio.Intermediates[0], &key.PublicKey, f.intermediateKey)
	if err != nil {
		t.Fatal(err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	return cert
}
