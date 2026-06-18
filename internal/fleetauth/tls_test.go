package fleetauth

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"math/big"
	"testing"
	"time"
)

// selfSigned returns a DER cert and a usable tls.Certificate for tests.
func selfSigned(t *testing.T) ([]byte, tls.Certificate) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "marshal-test"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	return der, tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}
}

func TestFingerprintIsSHA256Hex(t *testing.T) {
	der, _ := selfSigned(t)
	sum := sha256.Sum256(der)
	if got, want := Fingerprint(der), hex.EncodeToString(sum[:]); got != want {
		t.Fatalf("Fingerprint = %q, want %q", got, want)
	}
}

func TestClientTLSPinAcceptsMatch(t *testing.T) {
	der, _ := selfSigned(t)
	cfg, err := ClientTLS(Fingerprint(der), "")
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.InsecureSkipVerify {
		t.Fatal("pinned config must skip default verification")
	}
	if err := cfg.VerifyPeerCertificate([][]byte{der}, nil); err != nil {
		t.Fatalf("matching fingerprint rejected: %v", err)
	}
}

func TestClientTLSPinRejectsMismatch(t *testing.T) {
	der, _ := selfSigned(t)
	other, _ := selfSigned(t)
	cfg, err := ClientTLS(Fingerprint(other), "")
	if err != nil {
		t.Fatal(err)
	}
	if err := cfg.VerifyPeerCertificate([][]byte{der}, nil); err == nil {
		t.Fatal("mismatched fingerprint accepted")
	}
}

func TestClientTLSRequiresTrustSource(t *testing.T) {
	if _, err := ClientTLS("", ""); err == nil {
		t.Fatal("expected error when neither fingerprint nor caPath is set")
	}
}
