// Package fleetauth holds token and TLS primitives shared by the Marshal agent,
// CLI, and central server. It is pure (no I/O beyond reading a CA file) so all
// three callers can depend on it.
package fleetauth

import (
	"crypto/sha256"
	"crypto/subtle"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
)

// Fingerprint returns the lowercase hex SHA-256 of a certificate's DER bytes.
func Fingerprint(der []byte) string {
	sum := sha256.Sum256(der)
	return hex.EncodeToString(sum[:])
}

// ClientTLS builds a client TLS config. Exactly one trust source must be set:
// a pinned server-cert fingerprint, or a CA file path. Pinning skips default
// verification and instead matches the leaf cert's SHA-256 against fingerprint.
func ClientTLS(fingerprint, caPath string) (*tls.Config, error) {
	switch {
	case fingerprint != "" && caPath != "":
		return nil, errors.New("set either fingerprint or ca, not both")
	case fingerprint != "":
		want := fingerprint
		return &tls.Config{
			MinVersion:         tls.VersionTLS12,
			InsecureSkipVerify: true, // verification is done by the pin below
			VerifyPeerCertificate: func(rawCerts [][]byte, _ [][]*x509.Certificate) error {
				if len(rawCerts) == 0 {
					return errors.New("server presented no certificate")
				}
				got := Fingerprint(rawCerts[0])
				if subtle.ConstantTimeCompare([]byte(got), []byte(want)) != 1 {
					return fmt.Errorf("server cert fingerprint %s does not match pinned %s", got, want)
				}
				return nil
			},
		}, nil
	case caPath != "":
		pem, err := os.ReadFile(caPath)
		if err != nil {
			return nil, fmt.Errorf("read ca file: %w", err)
		}
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(pem) {
			return nil, fmt.Errorf("no certificates found in %s", caPath)
		}
		return &tls.Config{MinVersion: tls.VersionTLS12, RootCAs: pool}, nil
	default:
		return nil, errors.New("no TLS trust source: set fingerprint or ca")
	}
}
