# M10 — Fleet Auth & TLS Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make Marshal fleet mode require TLS + authentication on every RPC, with per-agent identity and server-side authorization of operator actions.

**Architecture:** A new pure `internal/fleetauth` package holds token generation/hashing and the client-side pinned-TLS config builder. The server generates/loads a self-signed cert and an `auth.json` (hashed enroll token, hashed admin token, per-agent registry), and enforces credentials in gRPC interceptors before any handler runs. Agents auto-enroll on first connect (enroll token → minted per-agent token persisted locally); the CLI sends a single admin token. No plaintext fallback.

**Tech Stack:** Go 1.26, gRPC (`google.golang.org/grpc`), `crypto/tls`, `crypto/x509`, `crypto/ecdsa`, `crypto/rand`, `crypto/sha256`, `crypto/subtle`, protobuf (`protoc` regen of `internal/pb`), cobra CLI.

## Global Constraints

- Go module path is `marshal`; imports are `marshal/internal/...`.
- TDD: failing test first, then minimal implementation. `go test ./... -race -count=1` must pass before every commit.
- `gofmt -l .` must print nothing; `go vet ./...` must be clean.
- Commit subject imperative; trailer `Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>`.
- All work on branch `m10-auth-tls` (already checked out).
- File modes: secrets and key material are written 0600; directories 0700.
- Fleet mode = `config.ServerConfig != nil`. Standalone mode (no `server:` block) MUST be unaffected — no TLS, no auth on the local daemon socket.
- No `--insecure` flag anywhere.
- Tokens: 32 bytes from `crypto/rand`, `base64.RawURLEncoding`. Hash: `sha256` hex. Compare: `crypto/subtle.ConstantTimeCompare`.
- Fingerprint: lowercase hex of `sha256(leaf cert DER)`, no separators.
- Metadata keys: `marshal-token` (per-agent OR admin token), `marshal-enroll` (enroll token, first connect only).

---

## File structure

**New files**
- `internal/fleetauth/token.go` — `GenerateToken`, `HashToken`, `VerifyToken`.
- `internal/fleetauth/token_test.go`
- `internal/fleetauth/tls.go` — `Fingerprint(der []byte) string`, `ClientTLS(fingerprint, caPath string) (*tls.Config, error)`.
- `internal/fleetauth/tls_test.go`
- `internal/server/tls.go` — `LoadOrCreateCert(dir, certPath, keyPath string) (tls.Certificate, string, error)`.
- `internal/server/tls_test.go`
- `internal/server/auth.go` — `authStore` (load/save `auth.json`), enroll/admin/agent verify + enroll/revoke/list.
- `internal/server/auth_test.go`
- `internal/server/interceptor.go` — unary + stream auth interceptors and the context-key accessors.
- `internal/server/interceptor_test.go`
- `cmd/marshal/server_auth.go` — `server fingerprint`, `server token`, `server agent` subcommands.
- `cmd/marshal/server_auth_test.go`

**Modified files**
- `proto/marshal/v1/fleet.proto` — `HelloAck.agent_token = 3`; regenerate `internal/pb`.
- `internal/config/config.go` — `ServerConfig` += `Token`, `Fingerprint`, `CA`; validation.
- `internal/store/store.go` — `FleetTokenPath`, `LoadFleetToken`, `SaveFleetToken`.
- `internal/fleet/client.go` — TLS creds + auth metadata + enrollment-token persistence.
- `internal/daemon/server.go` — pass TLS + token config into `fleet.New`.
- `internal/server/server.go` — TLS creds + interceptors in `Serve`/`ServeDir`; `Connect` enroll/auth handling; `NewServer` takes `*authStore`.
- `cmd/marshal/server.go` — first-run secret printing, `--tls-cert`/`--tls-key`.
- `cmd/marshal/fleet.go` — TLS + admin-token dials; `resolveServerAuth`.

---

# Phase 1 — TLS transport + fingerprint pinning

Goal: agent and CLI talk to the server over TLS with a pinned self-signed cert. No auth yet.

## Task 1: `fleetauth` TLS primitives (fingerprint + pinned client config)

**Files:**
- Create: `internal/fleetauth/tls.go`
- Test: `internal/fleetauth/tls_test.go`

**Interfaces:**
- Produces: `func Fingerprint(der []byte) string`; `func ClientTLS(fingerprint, caPath string) (*tls.Config, error)`.

- [ ] **Step 1: Write the failing test**

```go
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
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/fleetauth/ -run TestClientTLS -v`
Expected: FAIL — `undefined: ClientTLS` / `undefined: Fingerprint`.

- [ ] **Step 3: Write minimal implementation**

```go
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
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/fleetauth/ -v && gofmt -l internal/fleetauth/ && go vet ./internal/fleetauth/`
Expected: PASS; gofmt prints nothing.

- [ ] **Step 5: Commit**

```bash
git add internal/fleetauth/tls.go internal/fleetauth/tls_test.go
git commit -m "feat(fleetauth): fingerprint + pinned client TLS config

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

## Task 2: Server cert generation/loading

**Files:**
- Create: `internal/server/tls.go`
- Test: `internal/server/tls_test.go`

**Interfaces:**
- Consumes: `fleetauth.Fingerprint`.
- Produces: `func LoadOrCreateCert(dir, certPath, keyPath string) (tls.Certificate, string, error)` — returns the cert, its fingerprint hex, and error. If `certPath`/`keyPath` are empty, uses `dir/cert.pem` + `dir/key.pem`, generating a self-signed pair when absent.

- [ ] **Step 1: Write the failing test**

```go
package server

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadOrCreateCertGeneratesAndPersists(t *testing.T) {
	dir := t.TempDir()
	cert, fp, err := LoadOrCreateCert(dir, "", "")
	if err != nil {
		t.Fatal(err)
	}
	if fp == "" || len(cert.Certificate) == 0 {
		t.Fatal("empty cert or fingerprint")
	}
	for _, name := range []string{"cert.pem", "key.pem"} {
		p := filepath.Join(dir, name)
		info, err := os.Stat(p)
		if err != nil {
			t.Fatalf("missing %s: %v", name, err)
		}
		if info.Mode().Perm() != 0o600 {
			t.Fatalf("%s mode = %o, want 600", name, info.Mode().Perm())
		}
	}
}

func TestLoadOrCreateCertIsStableAcrossCalls(t *testing.T) {
	dir := t.TempDir()
	_, fp1, err := LoadOrCreateCert(dir, "", "")
	if err != nil {
		t.Fatal(err)
	}
	_, fp2, err := LoadOrCreateCert(dir, "", "")
	if err != nil {
		t.Fatal(err)
	}
	if fp1 != fp2 {
		t.Fatalf("fingerprint changed across calls: %s vs %s", fp1, fp2)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/server/ -run TestLoadOrCreateCert -v`
Expected: FAIL — `undefined: LoadOrCreateCert`.

- [ ] **Step 3: Write minimal implementation**

```go
package server

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"math/big"
	"os"
	"path/filepath"
	"time"

	"marshal/internal/fleetauth"
)

// LoadOrCreateCert returns a TLS certificate and its fingerprint. When certPath
// and keyPath are empty it uses dir/cert.pem and dir/key.pem, generating a
// self-signed pair (0600) if they do not exist.
func LoadOrCreateCert(dir, certPath, keyPath string) (tls.Certificate, string, error) {
	if certPath == "" {
		certPath = filepath.Join(dir, "cert.pem")
	}
	if keyPath == "" {
		keyPath = filepath.Join(dir, "key.pem")
	}
	if _, err := os.Stat(certPath); os.IsNotExist(err) {
		if err := generateSelfSigned(certPath, keyPath); err != nil {
			return tls.Certificate{}, "", err
		}
	}
	cert, err := tls.LoadX509KeyPair(certPath, keyPath)
	if err != nil {
		return tls.Certificate{}, "", fmt.Errorf("load key pair: %w", err)
	}
	return cert, fleetauth.Fingerprint(cert.Certificate[0]), nil
}

func generateSelfSigned(certPath, keyPath string) error {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return err
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(time.Now().UnixNano()),
		Subject:      pkix.Name{CommonName: "marshal-server"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(10 * 365 * 24 * time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		return err
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return err
	}
	if err := writePEM(certPath, "CERTIFICATE", der); err != nil {
		return err
	}
	return writePEM(keyPath, "EC PRIVATE KEY", keyDER)
}

func writePEM(path, blockType string, der []byte) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	return pem.Encode(f, &pem.Block{Type: blockType, Bytes: der})
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/server/ -run TestLoadOrCreateCert -v && gofmt -l internal/server/tls.go`
Expected: PASS; gofmt prints nothing.

- [ ] **Step 5: Commit**

```bash
git add internal/server/tls.go internal/server/tls_test.go
git commit -m "feat(server): self-signed cert generation and loading

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

## Task 3: Add `Fingerprint`/`CA` to agent config + validation

**Files:**
- Modify: `internal/config/config.go:67-71` (`ServerConfig`), `internal/config/config.go:138-141` (`validate`)
- Test: `internal/config/config_test.go` (add cases)

**Interfaces:**
- Produces: `ServerConfig{Address, Name, Fingerprint, CA string}` (Token added in Phase 3).

- [ ] **Step 1: Write the failing test**

Add to `internal/config/config_test.go`:

```go
func TestServerRequiresTrustSource(t *testing.T) {
	_, err := Parse([]byte("server:\n  address: h:9000\napps:\n  - name: a\n    cmd: echo\n"))
	if err == nil {
		t.Fatal("expected error: server block needs fingerprint or ca")
	}
}

func TestServerRejectsBothTrustSources(t *testing.T) {
	_, err := Parse([]byte("server:\n  address: h:9000\n  fingerprint: abc\n  ca: /x.pem\napps:\n  - name: a\n    cmd: echo\n"))
	if err == nil {
		t.Fatal("expected error: fingerprint and ca are mutually exclusive")
	}
}

func TestServerWithFingerprintParses(t *testing.T) {
	cfg, err := Parse([]byte("server:\n  address: h:9000\n  fingerprint: abc\napps:\n  - name: a\n    cmd: echo\n"))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Server.Fingerprint != "abc" {
		t.Fatalf("fingerprint = %q", cfg.Server.Fingerprint)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/config/ -run TestServer -v`
Expected: FAIL — `TestServerRequiresTrustSource` does not error (and `Fingerprint` field undefined).

- [ ] **Step 3: Write minimal implementation**

Replace `ServerConfig` (lines 67-71):

```go
// ServerConfig points the agent at a central server. Presence enables fleet mode.
type ServerConfig struct {
	Address     string `yaml:"address" json:"address"`
	Name        string `yaml:"name" json:"name,omitempty"`
	Token       string `yaml:"token" json:"token,omitempty"`             // enrollment token (used until enrolled)
	Fingerprint string `yaml:"fingerprint" json:"fingerprint,omitempty"` // pinned server cert SHA-256
	CA          string `yaml:"ca" json:"ca,omitempty"`                   // CA file path (alternative to fingerprint)
}
```

Replace the server check in `validate` (lines 138-141):

```go
	if c.Server != nil {
		if c.Server.Address == "" {
			return fmt.Errorf("server.address is required when a server block is present")
		}
		if c.Server.Fingerprint != "" && c.Server.CA != "" {
			return fmt.Errorf("server.fingerprint and server.ca are mutually exclusive")
		}
		if c.Server.Fingerprint == "" && c.Server.CA == "" {
			return fmt.Errorf("server needs a trust source: set server.fingerprint or server.ca")
		}
	}
```

(`Token` is declared now so Phase 3 needs no further struct change; its required-unless-enrolled check is added in Phase 3, Task 11.)

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/config/ -v && gofmt -l internal/config/config.go && go vet ./internal/config/`
Expected: PASS.

> **Note for the implementer:** other tests/fixtures in the repo that build a `server:` block without a trust source will now fail. Grep `rg -l "server:" --glob '*_test.go' --glob '*.yaml'` and add `fingerprint: <any>` to each fleet-mode fixture. Run `go test ./... 2>&1 | grep -A2 FAIL` and fix every failure before committing.

- [ ] **Step 5: Commit**

```bash
git add internal/config/
git commit -m "feat(config): server trust-source fields (fingerprint/ca) + validation

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

## Task 4: Serve over TLS + first-run fingerprint print

**Files:**
- Modify: `internal/server/server.go:294-308` (`Serve`), `:312` (`ServeDir`)
- Modify: `cmd/marshal/server.go` (`--tls-cert`/`--tls-key`, print fingerprint)
- Test: `internal/server/server_test.go` or existing e2e harness (add a TLS dial helper)

**Interfaces:**
- Consumes: `LoadOrCreateCert`, `fleetauth.ClientTLS`.
- Produces: `Serve(ctx, lis, reg, ss, ls, tlsCert tls.Certificate) error` (new trailing param); `ServeDir(ctx, lis, dataDir, opts...) error` now loads the cert from `dataDir` and returns the fingerprint via a printed log line — keep its signature but have it call `LoadOrCreateCert` internally.

- [ ] **Step 1: Write the failing test**

Add `internal/server/tls_serve_test.go`:

```go
package server

import (
	"context"
	"net"
	"testing"
	"time"

	"marshal/internal/fleetauth"
	"marshal/internal/pb"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
)

func TestServeOverTLS(t *testing.T) {
	dir := t.TempDir()
	cert, fp, err := LoadOrCreateCert(dir, "", "")
	if err != nil {
		t.Fatal(err)
	}
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go Serve(ctx, lis, NewRegistry(), nil, nil, cert)

	cfg, err := fleetauth.ClientTLS(fp, "")
	if err != nil {
		t.Fatal(err)
	}
	conn, err := grpc.NewClient(lis.Addr().String(), grpc.WithTransportCredentials(credentials.NewTLS(cfg)))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	dctx, dcancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer dcancel()
	if _, err := pb.NewFleetClient(conn).ListFleet(dctx, &pb.ListFleetRequest{}); err != nil {
		t.Fatalf("ListFleet over TLS failed: %v", err)
	}
}
```

(Use the real registry constructor name — check `internal/server` for `NewRegistry`; if it differs, match it.)

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/server/ -run TestServeOverTLS -v`
Expected: FAIL — `Serve` takes wrong number of args / compile error.

- [ ] **Step 3: Write minimal implementation**

In `internal/server/server.go`, change `Serve` to accept the cert and serve TLS:

```go
import (
	"crypto/tls"
	// ...existing imports...
	"google.golang.org/grpc/credentials"
)

// Serve registers the Fleet service on lis (TLS) and serves until ctx is canceled.
func Serve(ctx context.Context, lis net.Listener, reg *Registry, ss *stores, ls *logStores, cert tls.Certificate) error {
	creds := credentials.NewTLS(&tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12})
	gs := grpc.NewServer(grpc.Creds(creds))
	pb.RegisterFleetServer(gs, NewServer(reg, ss, ls))
	go func() {
		<-ctx.Done()
		gs.GracefulStop()
		if ss != nil {
			_ = ss.closeAll()
		}
		if ls != nil {
			_ = ls.closeAll()
		}
	}()
	return gs.Serve(lis)
}
```

In `ServeDir`, load the cert from `dataDir` and pass it to `Serve`. Add near the top of `ServeDir` after `os.MkdirAll`:

```go
	cert, fp, err := LoadOrCreateCert(dataDir, "", "")
	if err != nil {
		return err
	}
	log.Printf("fleet: server cert fingerprint %s", fp)
```

and change the final `Serve(...)` call inside `ServeDir` to pass `cert`. (If `ServeDir` currently calls `Serve`, update that call; if it builds its own `grpc.Server`, mirror the `grpc.Creds` change there.)

In `cmd/marshal/server.go`, add flags and print the fingerprint on startup. After resolving `dataDir`:

```go
	cert, fp, err := server.LoadOrCreateCert(dataDir, tlsCert, tlsKey)
	if err != nil {
		return fmt.Errorf("load tls cert: %w", err)
	}
	_ = cert // ServeDir reloads from dataDir; for custom paths we print here
	fmt.Fprintf(cmd.OutOrStdout(), "marshal server: cert fingerprint %s\n", fp)
```

and register:

```go
	cmd.Flags().StringVar(&tlsCert, "tls-cert", "", "path to TLS cert PEM (default <data-dir>/cert.pem, generated if absent)")
	cmd.Flags().StringVar(&tlsKey, "tls-key", "", "path to TLS key PEM (default <data-dir>/key.pem)")
```

> If `--tls-cert`/`--tls-key` are set, `ServeDir` must honor them. Add an option to `ServeDir` (via the existing `RegOption`/opts mechanism, or a new `ServeDir(ctx, lis, dataDir, certPath, keyPath, opts...)`) so the custom paths flow through. Simplest: change `LoadOrCreateCert(dataDir, "", "")` inside `ServeDir` to read the paths from a package-level option set by a new `WithTLSPaths(cert, key string) RegOption`. Implement whichever matches the existing `RegOption` pattern in `internal/server`.

- [ ] **Step 4: Migrate existing insecure server/test harnesses**

Run: `rg -n "insecure.NewCredentials|grpc.NewServer\(\)" internal/server cmd/marshal`
For each Fleet-service test that dials insecure, switch the server to `Serve(..., cert)` (or a TLS test harness) and the client to `credentials.NewTLS(fleetauth.ClientTLS(fp, ""))`. Add a shared test helper `newTLSTestServer(t)` returning `(addr, fingerprint string)` to avoid duplication.

- [ ] **Step 5: Run the full suite**

Run: `go test ./... -race -count=1 2>&1 | tail -20`
Expected: PASS (after migrating the harnesses; the agent client in Task 5 is not yet TLS, so `internal/daemon` fleet round-trip tests may need Task 5 — if so, mark them `t.Skip("TLS agent client lands in Task 5")` here and remove the skip in Task 5).

- [ ] **Step 6: Commit**

```bash
git add internal/server/ cmd/marshal/server.go
git commit -m "feat(server): serve Fleet over TLS; print cert fingerprint

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

## Task 5: Agent client + CLI dial over TLS

**Files:**
- Modify: `internal/fleet/client.go:32-43` (`Client` struct), `:110` (dial)
- Modify: `internal/daemon/server.go:264-268` (pass TLS opt)
- Modify: `cmd/marshal/fleet.go` (all four dials → TLS)
- Test: `internal/fleet/client_test.go` (extend round-trip to TLS)

**Interfaces:**
- Produces: `fleet.WithTLS(cfg *tls.Config) Option`; `resolveServerAuth(...)` in the CLI returning `(addr, fingerprint string)` (token added Phase 2).

- [ ] **Step 1: Write the failing test**

Extend the existing fleet client round-trip test to start the server with TLS and dial with `WithTLS`. Add to `internal/fleet/client_test.go` a variant that builds `fleetauth.ClientTLS(fp, "")` and passes `fleet.WithTLS(cfg)`; assert a snapshot reaches the server. (Model it on the existing round-trip test in this file.)

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/fleet/ -v`
Expected: FAIL — `undefined: WithTLS`, or dial fails against the now-TLS server.

- [ ] **Step 3: Write minimal implementation**

In `internal/fleet/client.go`:

```go
import (
	"crypto/tls"
	// ...
	"google.golang.org/grpc/credentials"
	// remove: "google.golang.org/grpc/credentials/insecure"
)

// add field
type Client struct {
	// ...existing fields...
	tls *tls.Config
}

// WithTLS sets the client TLS config (pinned fingerprint or CA). Required in
// fleet mode; there is no insecure fallback.
func WithTLS(cfg *tls.Config) Option { return func(c *Client) { c.tls = cfg } }
```

In `connectOnce` replace the dial:

```go
	if c.tls == nil {
		return false, errors.New("fleet: TLS config required")
	}
	conn, err := grpc.NewClient(c.addr, grpc.WithTransportCredentials(credentials.NewTLS(c.tls)))
```

In `internal/daemon/server.go` build the TLS config from `sc` and pass it. Replace the `fleet.New(...)` block (lines 264-268):

```go
	tlsCfg, tErr := fleetauth.ClientTLS(sc.Fingerprint, sc.CA)
	if tErr != nil {
		log.Printf("fleet: disabled, bad TLS config: %v", tErr)
	} else {
		fc := fleet.New(sc.Address, name, version.String(),
			fleetSnapshot(mgr, sampler),
			fleet.WithTLS(tlsCfg),
			fleet.WithMetrics(metricsSince(mdb)),
			fleet.WithLogs(logsSince(reg)),
			fleet.WithCommands(srv.handleFleetCommand))
		go fc.Run(serveCtx)
	}
```

(Add `"marshal/internal/fleetauth"` and `"log"` imports as needed.)

In `cmd/marshal/fleet.go`, add `resolveServerAuth` and replace all four insecure dials. First the resolver:

```go
// resolveServerAuth resolves the server address and pinned fingerprint from
// flags, then env (MARSHAL_SERVER / MARSHAL_FINGERPRINT). Token is added in a
// later task.
func resolveServerAuth(serverFlag, fpFlag string) (addr, fingerprint string) {
	addr = resolveServer(serverFlag)
	fingerprint = fpFlag
	if fingerprint == "" {
		fingerprint = os.Getenv("MARSHAL_FINGERPRINT")
	}
	return addr, fingerprint
}

// dialFleet builds a TLS gRPC client connection to the server.
func dialFleet(addr, fingerprint string) (*grpc.ClientConn, error) {
	cfg, err := fleetauth.ClientTLS(fingerprint, "")
	if err != nil {
		return nil, err
	}
	return grpc.NewClient(addr, grpc.WithTransportCredentials(credentials.NewTLS(cfg)))
}
```

Add a `--fingerprint` flag to each fleet subcommand and replace each
`grpc.NewClient(resolveServer(serverAddr), grpc.WithTransportCredentials(insecure.NewCredentials()))`
with:

```go
	addr, fp := resolveServerAuth(serverAddr, fingerprintFlag)
	conn, err := dialFleet(addr, fp)
```

Update imports: drop `insecure`, add `"google.golang.org/grpc/credentials"` and `"marshal/internal/fleetauth"`.

- [ ] **Step 4: Run tests**

Run: `go test ./internal/fleet/ ./internal/daemon/ ./cmd/marshal/ -race -count=1 2>&1 | tail -20`
Expected: PASS. Remove any `t.Skip` added in Task 4.

- [ ] **Step 5: Full suite + lint**

Run: `go test ./... -race -count=1 && gofmt -l . && go vet ./...`
Expected: PASS; gofmt silent.

- [ ] **Step 6: Commit**

```bash
git add internal/fleet/ internal/daemon/server.go cmd/marshal/fleet.go
git commit -m "feat(fleet): agent + CLI dial server over pinned TLS

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

# Phase 2 — Auth interceptor + admin token

Goal: server holds hashed enroll + admin tokens; operator RPCs require the admin token.

## Task 6: `fleetauth` token primitives

**Files:**
- Create: `internal/fleetauth/token.go`
- Test: `internal/fleetauth/token_test.go`

**Interfaces:**
- Produces: `func GenerateToken() (string, error)`; `func HashToken(token string) string`; `func VerifyToken(token, hash string) bool`.

- [ ] **Step 1: Write the failing test**

```go
package fleetauth

import "testing"

func TestGenerateTokenIsRandomAndURLSafe(t *testing.T) {
	a, err := GenerateToken()
	if err != nil {
		t.Fatal(err)
	}
	b, err := GenerateToken()
	if err != nil {
		t.Fatal(err)
	}
	if a == b {
		t.Fatal("tokens are not random")
	}
	if len(a) < 32 {
		t.Fatalf("token too short: %d", len(a))
	}
}

func TestHashVerifyRoundTrip(t *testing.T) {
	tok, _ := GenerateToken()
	h := HashToken(tok)
	if h == tok {
		t.Fatal("hash equals token")
	}
	if !VerifyToken(tok, h) {
		t.Fatal("correct token did not verify")
	}
	if VerifyToken(tok+"x", h) {
		t.Fatal("wrong token verified")
	}
	if VerifyToken("", "") {
		t.Fatal("empty token/hash verified")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/fleetauth/ -run TestGenerate -v`
Expected: FAIL — `undefined: GenerateToken`.

- [ ] **Step 3: Write minimal implementation**

```go
package fleetauth

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
)

// GenerateToken returns a 32-byte random token, base64url (no padding).
func GenerateToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// HashToken returns the hex SHA-256 of a token, for storage at rest.
func HashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// VerifyToken reports whether token hashes to hash (constant time). Empty
// token or hash always returns false.
func VerifyToken(token, hash string) bool {
	if token == "" || hash == "" {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(HashToken(token)), []byte(hash)) == 1
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/fleetauth/ -v && gofmt -l internal/fleetauth/`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/fleetauth/token.go internal/fleetauth/token_test.go
git commit -m "feat(fleetauth): token generate/hash/verify

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

## Task 7: Server `authStore` (auth.json) — secrets + agent registry

**Files:**
- Create: `internal/server/auth.go`
- Test: `internal/server/auth_test.go`

**Interfaces:**
- Consumes: `fleetauth.GenerateToken/HashToken/VerifyToken`.
- Produces:
  - `type authStore struct{...}` with `path string`, mutex, and in-memory `authData`.
  - `func loadOrInitAuth(dir string) (*authStore, *initSecrets, error)` — loads `dir/auth.json`; if absent, generates enroll+admin tokens, persists their hashes, and returns the plaintext in `initSecrets` (so the CLI can print once). `initSecrets` is nil when the file already existed.
  - `type initSecrets struct{ EnrollToken, AdminToken string }`
  - `func (a *authStore) verifyAdmin(token string) bool`
  - `func (a *authStore) verifyEnroll(token string) bool`
  - `func (a *authStore) enrollAgent(name string) (token string, err error)` — mints + persists a per-agent token; errors if name already bound.
  - `func (a *authStore) authAgent(token string) (name string, ok bool)` — resolves a per-agent token to its bound name.
  - `func (a *authStore) listAgents() []agentEntry` / `func (a *authStore) removeAgent(name string) bool`
  - `func (a *authStore) rotate(which string) (string, error)` — which ∈ {"enroll","admin"}; returns new plaintext.

- [ ] **Step 1: Write the failing test**

```go
package server

import (
	"path/filepath"
	"testing"
)

func TestLoadOrInitAuthGeneratesSecretsOnce(t *testing.T) {
	dir := t.TempDir()
	a, secrets, err := loadOrInitAuth(dir)
	if err != nil {
		t.Fatal(err)
	}
	if secrets == nil || secrets.EnrollToken == "" || secrets.AdminToken == "" {
		t.Fatal("expected fresh secrets on first init")
	}
	if !a.verifyAdmin(secrets.AdminToken) || !a.verifyEnroll(secrets.EnrollToken) {
		t.Fatal("generated secrets do not verify")
	}
	if a.verifyAdmin(secrets.EnrollToken) {
		t.Fatal("enroll token must not pass as admin")
	}
	// Reload: existing file, no new secrets, same verification.
	a2, secrets2, err := loadOrInitAuth(dir)
	if err != nil {
		t.Fatal(err)
	}
	if secrets2 != nil {
		t.Fatal("expected nil secrets on reload")
	}
	if !a2.verifyAdmin(secrets.AdminToken) {
		t.Fatal("admin token broke across reload")
	}
	if _, err := filepath.Abs(a.path); err != nil {
		t.Fatal(err)
	}
}

func TestEnrollAndAuthAgent(t *testing.T) {
	dir := t.TempDir()
	a, _, err := loadOrInitAuth(dir)
	if err != nil {
		t.Fatal(err)
	}
	tok, err := a.enrollAgent("dev-1")
	if err != nil {
		t.Fatal(err)
	}
	if name, ok := a.authAgent(tok); !ok || name != "dev-1" {
		t.Fatalf("authAgent = %q,%v", name, ok)
	}
	if _, err := a.enrollAgent("dev-1"); err == nil {
		t.Fatal("re-enrolling an existing name must error")
	}
	// Survives reload.
	a2, _, err := loadOrInitAuth(dir)
	if err != nil {
		t.Fatal(err)
	}
	if name, ok := a2.authAgent(tok); !ok || name != "dev-1" {
		t.Fatalf("agent token broke across reload: %q,%v", name, ok)
	}
	if !a2.removeAgent("dev-1") {
		t.Fatal("removeAgent should report true")
	}
	if _, ok := a2.authAgent(tok); ok {
		t.Fatal("revoked agent still authenticates")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/server/ -run "TestLoadOrInitAuth|TestEnrollAndAuthAgent" -v`
Expected: FAIL — `undefined: loadOrInitAuth`.

- [ ] **Step 3: Write minimal implementation**

```go
package server

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"marshal/internal/fleetauth"
)

type agentEntry struct {
	TokenHash  string `json:"token_hash"`
	EnrolledAt int64  `json:"enrolled_at"`
}

type authData struct {
	EnrollTokenHash string                `json:"enroll_token_hash"`
	AdminTokenHash  string                `json:"admin_token_hash"`
	Agents          map[string]agentEntry `json:"agents"`
}

type authStore struct {
	path string
	mu   sync.Mutex
	data authData
}

type initSecrets struct {
	EnrollToken string
	AdminToken  string
}

func loadOrInitAuth(dir string) (*authStore, *initSecrets, error) {
	path := filepath.Join(dir, "auth.json")
	a := &authStore{path: path, data: authData{Agents: map[string]agentEntry{}}}
	b, err := os.ReadFile(path)
	if err == nil {
		if err := json.Unmarshal(b, &a.data); err != nil {
			return nil, nil, fmt.Errorf("parse auth.json: %w", err)
		}
		if a.data.Agents == nil {
			a.data.Agents = map[string]agentEntry{}
		}
		return a, nil, nil
	}
	if !os.IsNotExist(err) {
		return nil, nil, err
	}
	enroll, err := fleetauth.GenerateToken()
	if err != nil {
		return nil, nil, err
	}
	admin, err := fleetauth.GenerateToken()
	if err != nil {
		return nil, nil, err
	}
	a.data.EnrollTokenHash = fleetauth.HashToken(enroll)
	a.data.AdminTokenHash = fleetauth.HashToken(admin)
	if err := a.save(); err != nil {
		return nil, nil, err
	}
	return a, &initSecrets{EnrollToken: enroll, AdminToken: admin}, nil
}

// save writes auth.json atomically (0600). Caller holds a.mu, or it is called
// during init before the store is shared.
func (a *authStore) save() error {
	b, err := json.MarshalIndent(a.data, "", "  ")
	if err != nil {
		return err
	}
	tmp := a.path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, a.path)
}

func (a *authStore) verifyAdmin(token string) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return fleetauth.VerifyToken(token, a.data.AdminTokenHash)
}

func (a *authStore) verifyEnroll(token string) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return fleetauth.VerifyToken(token, a.data.EnrollTokenHash)
}

func (a *authStore) enrollAgent(name string) (string, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if _, exists := a.data.Agents[name]; exists {
		return "", fmt.Errorf("agent %q already enrolled", name)
	}
	tok, err := fleetauth.GenerateToken()
	if err != nil {
		return "", err
	}
	a.data.Agents[name] = agentEntry{TokenHash: fleetauth.HashToken(tok), EnrolledAt: time.Now().Unix()}
	if err := a.save(); err != nil {
		delete(a.data.Agents, name)
		return "", err
	}
	return tok, nil
}

func (a *authStore) authAgent(token string) (string, bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	for name, e := range a.data.Agents {
		if fleetauth.VerifyToken(token, e.TokenHash) {
			return name, true
		}
	}
	return "", false
}

func (a *authStore) removeAgent(name string) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	if _, ok := a.data.Agents[name]; !ok {
		return false
	}
	delete(a.data.Agents, name)
	_ = a.save()
	return true
}

type listedAgent struct {
	Name       string
	EnrolledAt int64
}

func (a *authStore) listAgents() []listedAgent {
	a.mu.Lock()
	defer a.mu.Unlock()
	out := make([]listedAgent, 0, len(a.data.Agents))
	for name, e := range a.data.Agents {
		out = append(out, listedAgent{Name: name, EnrolledAt: e.EnrolledAt})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

func (a *authStore) rotate(which string) (string, error) {
	tok, err := fleetauth.GenerateToken()
	if err != nil {
		return "", err
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	switch which {
	case "enroll":
		a.data.EnrollTokenHash = fleetauth.HashToken(tok)
	case "admin":
		a.data.AdminTokenHash = fleetauth.HashToken(tok)
	default:
		return "", fmt.Errorf("unknown token %q (want enroll|admin)", which)
	}
	if err := a.save(); err != nil {
		return "", err
	}
	return tok, nil
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/server/ -run "TestLoadOrInitAuth|TestEnrollAndAuthAgent" -v && gofmt -l internal/server/auth.go`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/server/auth.go internal/server/auth_test.go
git commit -m "feat(server): auth.json store — secrets + agent registry

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

## Task 8: Auth interceptors (admin on operator RPCs)

**Files:**
- Create: `internal/server/interceptor.go`
- Test: `internal/server/interceptor_test.go`

**Interfaces:**
- Consumes: `authStore.verifyAdmin`, `authStore.verifyEnroll`, `authStore.authAgent`.
- Produces:
  - `func (a *authStore) unaryAuth(ctx, req, info, handler) (any, error)` — requires `marshal-token` == admin on all unary RPCs.
  - `func (a *authStore) streamAuth(srv, ss, info, handler) error` — for `Connect`: requires a valid `marshal-token` (per-agent) OR `marshal-enroll`; injects the resolved identity into the stream context.
  - `func authedAgentName(ctx) (string, bool)` and `func isEnrolling(ctx) bool` — accessors for the Connect handler.

- [ ] **Step 1: Write the failing test**

```go
package server

import (
	"context"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

func TestUnaryAuthRejectsMissingToken(t *testing.T) {
	dir := t.TempDir()
	a, secrets, _ := loadOrInitAuth(dir)
	info := &grpc.UnaryServerInfo{FullMethod: "/marshal.v1.Fleet/ListFleet"}
	called := false
	h := func(ctx context.Context, req any) (any, error) { called = true; return "ok", nil }

	_, err := a.unaryAuth(context.Background(), nil, info, h)
	if status.Code(err) != codes.Unauthenticated {
		t.Fatalf("missing token: code = %v, want Unauthenticated", status.Code(err))
	}
	if called {
		t.Fatal("handler ran without auth")
	}

	bad := metadata.NewIncomingContext(context.Background(), metadata.Pairs("marshal-token", "nope"))
	if _, err := a.unaryAuth(bad, nil, info, h); status.Code(err) != codes.PermissionDenied {
		t.Fatalf("bad token: code = %v, want PermissionDenied", status.Code(err))
	}

	good := metadata.NewIncomingContext(context.Background(), metadata.Pairs("marshal-token", secrets.AdminToken))
	if _, err := a.unaryAuth(good, nil, info, h); err != nil {
		t.Fatalf("valid admin token rejected: %v", err)
	}
	if !called {
		t.Fatal("handler did not run with valid token")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/server/ -run TestUnaryAuth -v`
Expected: FAIL — `undefined: (*authStore).unaryAuth`.

- [ ] **Step 3: Write minimal implementation**

```go
package server

import (
	"context"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

type ctxKey int

const (
	keyAgentName ctxKey = iota
	keyEnrolling
)

func metaToken(ctx context.Context, key string) string {
	md, ok := metadata.FromIncomingContext(ctx)
	if !ok {
		return ""
	}
	vals := md.Get(key)
	if len(vals) == 0 {
		return ""
	}
	return vals[0]
}

// unaryAuth requires a valid admin token on every unary operator RPC.
func (a *authStore) unaryAuth(ctx context.Context, req any, _ *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
	tok := metaToken(ctx, "marshal-token")
	if tok == "" {
		return nil, status.Error(codes.Unauthenticated, "missing admin token")
	}
	if !a.verifyAdmin(tok) {
		return nil, status.Error(codes.PermissionDenied, "invalid admin token")
	}
	return handler(ctx, req)
}

type wrappedStream struct {
	grpc.ServerStream
	ctx context.Context
}

func (w *wrappedStream) Context() context.Context { return w.ctx }

// streamAuth authenticates the Connect stream: a valid per-agent token resolves
// to its bound identity; otherwise a valid enroll token permits enrollment.
func (a *authStore) streamAuth(srv any, ss grpc.ServerStream, _ *grpc.StreamServerInfo, handler grpc.StreamHandler) error {
	ctx := ss.Context()
	if tok := metaToken(ctx, "marshal-token"); tok != "" {
		if name, ok := a.authAgent(tok); ok {
			ctx = context.WithValue(ctx, keyAgentName, name)
			return handler(srv, &wrappedStream{ServerStream: ss, ctx: ctx})
		}
		return status.Error(codes.PermissionDenied, "invalid agent token")
	}
	if enroll := metaToken(ctx, "marshal-enroll"); enroll != "" {
		if a.verifyEnroll(enroll) {
			ctx = context.WithValue(ctx, keyEnrolling, true)
			return handler(srv, &wrappedStream{ServerStream: ss, ctx: ctx})
		}
		return status.Error(codes.PermissionDenied, "invalid enrollment token")
	}
	return status.Error(codes.Unauthenticated, "missing agent or enrollment token")
}

func authedAgentName(ctx context.Context) (string, bool) {
	name, ok := ctx.Value(keyAgentName).(string)
	return name, ok
}

func isEnrolling(ctx context.Context) bool {
	v, _ := ctx.Value(keyEnrolling).(bool)
	return v
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/server/ -run TestUnaryAuth -v && gofmt -l internal/server/interceptor.go`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/server/interceptor.go internal/server/interceptor_test.go
git commit -m "feat(server): auth interceptors (admin unary, agent/enroll stream)

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

## Task 9: Wire interceptors + auth into the server; CLI sends admin token

**Files:**
- Modify: `internal/server/server.go` (`NewServer`, `Serve`, `ServeDir`)
- Modify: `cmd/marshal/server.go` (print admin/enroll secrets on first run)
- Modify: `cmd/marshal/fleet.go` (`resolveServerAuth` returns token; attach metadata)
- Test: extend `internal/server` e2e to assert unauthenticated operator RPCs are rejected; update fleet client/CLI test harnesses to pass tokens.

**Interfaces:**
- Produces: `NewServer(reg, ss, ls, auth *authStore)`; `Serve(ctx, lis, reg, ss, ls, cert, auth)`. CLI helper `resolveServerAuth(serverFlag, fpFlag, tokenFlag string) (addr, fingerprint, token string)` and metadata attach on every operator call.

- [ ] **Step 1: Write the failing test**

Add to the server e2e test file:

```go
func TestOperatorRPCRequiresAdminToken(t *testing.T) {
	dir := t.TempDir()
	cert, fp, _ := LoadOrCreateCert(dir, "", "")
	auth, secrets, _ := loadOrInitAuth(dir)
	lis, _ := net.Listen("tcp", "127.0.0.1:0")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go Serve(ctx, lis, NewRegistry(), nil, nil, cert, auth)

	cfg, _ := fleetauth.ClientTLS(fp, "")
	conn, _ := grpc.NewClient(lis.Addr().String(), grpc.WithTransportCredentials(credentials.NewTLS(cfg)))
	defer conn.Close()
	cl := pb.NewFleetClient(conn)

	// No token -> Unauthenticated.
	if _, err := cl.ListFleet(context.Background(), &pb.ListFleetRequest{}); status.Code(err) != codes.Unauthenticated {
		t.Fatalf("no token: %v", status.Code(err))
	}
	// Valid admin token -> ok.
	md := metadata.AppendToOutgoingContext(context.Background(), "marshal-token", secrets.AdminToken)
	if _, err := cl.ListFleet(md, &pb.ListFleetRequest{}); err != nil {
		t.Fatalf("admin token rejected: %v", err)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/server/ -run TestOperatorRPCRequiresAdminToken -v`
Expected: FAIL — `Serve`/`NewServer` arity, then auth not enforced.

- [ ] **Step 3: Write minimal implementation**

In `internal/server/server.go`:

```go
type Server struct {
	pb.UnimplementedFleetServer
	reg    *Registry
	stores *stores
	logs   *logStores
	broker *broker
	auth   *authStore
}

func NewServer(reg *Registry, ss *stores, ls *logStores, auth *authStore) *Server {
	return &Server{reg: reg, stores: ss, logs: ls, broker: newBroker(), auth: auth}
}

func Serve(ctx context.Context, lis net.Listener, reg *Registry, ss *stores, ls *logStores, cert tls.Certificate, auth *authStore) error {
	creds := credentials.NewTLS(&tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12})
	gs := grpc.NewServer(
		grpc.Creds(creds),
		grpc.UnaryInterceptor(auth.unaryAuth),
		grpc.StreamInterceptor(auth.streamAuth),
	)
	pb.RegisterFleetServer(gs, NewServer(reg, ss, ls, auth))
	// ...unchanged shutdown goroutine + return gs.Serve(lis)...
}
```

In `ServeDir`, after loading the cert, init auth and pass it through:

```go
	auth, secrets, err := loadOrInitAuth(dataDir)
	if err != nil {
		return err
	}
	if secrets != nil {
		log.Printf("fleet: generated enroll token %s", secrets.EnrollToken)
		log.Printf("fleet: generated admin token %s", secrets.AdminToken)
	}
	// pass cert + auth into Serve(...)
```

(Update the `Serve(...)` call inside `ServeDir` to pass `cert, auth`.)

Update **all** existing callers of `NewServer`/`Serve` in tests to the new arity.

In `cmd/marshal/server.go`, on first run, print the secrets to stdout (not just the server log) so the operator captures them. Since `loadOrInitAuth` runs inside `ServeDir`, expose them: simplest is to call `server.LoadOrInitAuthPrint(dataDir, cmd.OutOrStdout())` — add a thin exported wrapper in `internal/server` that runs `loadOrInitAuth` and prints secrets if fresh, returning the `*authStore` for `ServeDir` to reuse. To avoid double-init, refactor `ServeDir` to accept an already-built `*authStore` via a `RegOption`, OR have `ServeDir` print to a writer passed as an option. Choose the approach that fits the existing `RegOption` pattern; the requirement is: **fresh secrets print to the server command's stdout exactly once.**

In `cmd/marshal/fleet.go`, extend `resolveServerAuth` to also resolve the token and attach it:

```go
func resolveServerAuth(serverFlag, fpFlag, tokenFlag string) (addr, fingerprint, token string) {
	addr = resolveServer(serverFlag)
	fingerprint = firstNonEmpty(fpFlag, os.Getenv("MARSHAL_FINGERPRINT"))
	token = firstNonEmpty(tokenFlag, os.Getenv("MARSHAL_TOKEN"))
	return
}

// authCtx attaches the admin token to outgoing metadata.
func authCtx(parent context.Context, token string) context.Context {
	return metadata.AppendToOutgoingContext(parent, "marshal-token", token)
}
```

Add a `--token` flag to each fleet subcommand, resolve `(addr, fp, token)`, dial with `dialFleet(addr, fp)`, and wrap the RPC context with `authCtx(ctx, token)`. Add `firstNonEmpty` helper if not present. Import `"google.golang.org/grpc/metadata"`.

- [ ] **Step 4: Migrate harnesses + run suite**

Update the fleet client/CLI test harnesses to enroll (Phase 3) or, for Phase 2, to pass the admin token / a registered agent token where needed. For the agent-client round-trip test, the stream now needs `marshal-enroll` or `marshal-token` metadata — temporarily attach a valid enroll token via `metadata.AppendToOutgoingContext` in the test dial until Task 10 makes the client do it itself.

Run: `go test ./... -race -count=1 2>&1 | tail -20`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/server/ cmd/marshal/server.go cmd/marshal/fleet.go
git commit -m "feat(server): enforce auth interceptors; CLI sends admin token

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

# Phase 3 — Agent enrollment + per-agent identity

Goal: `HelloAck.agent_token`; agent enrolls on first connect, persists the minted token, authenticates with it thereafter; server trusts the authenticated name.

## Task 10: Proto — `HelloAck.agent_token`

**Files:**
- Modify: `proto/marshal/v1/fleet.proto` (`HelloAck`)
- Regenerate: `internal/pb/*`

**Interfaces:**
- Produces: `pb.HelloAck.GetAgentToken() string`.

- [ ] **Step 1: Edit the proto**

In `HelloAck`:

```proto
message HelloAck {
  int64  last_metric_ts_ms = 1;
  int64  last_log_ts_ms    = 2;
  string agent_token       = 3; // M10: minted per-agent token (set only on enrollment)
}
```

- [ ] **Step 2: Regenerate**

Run: `go generate ./internal/pb` (the repo's regen directive; needs `protoc` on PATH plus the `protoc-gen-go`/`protoc-gen-go-grpc` plugins listed in `go.mod`).
Expected: `internal/pb/fleet.pb.go` now has `AgentToken`.

- [ ] **Step 3: Verify it compiles**

Run: `go build ./... && go test ./internal/pb/... 2>&1 | tail -5`
Expected: builds; `pb.HelloAck{}.GetAgentToken()` exists (confirm with `rg "func .*HelloAck.*GetAgentToken" internal/pb`).

- [ ] **Step 4: Commit**

```bash
git add proto/ internal/pb/
git commit -m "proto: HelloAck.agent_token for M10 enrollment

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

## Task 11: Agent store — persist the minted token; config requires token unless enrolled

**Files:**
- Modify: `internal/store/store.go`
- Modify: `internal/config/config.go` (`validate` — Token required unless a `fleet-token` exists; see note)
- Test: `internal/store/store_test.go`

**Interfaces:**
- Produces: `func (s *Store) FleetTokenPath() string`; `func (s *Store) LoadFleetToken() (string, error)` (missing → `("", nil)`); `func (s *Store) SaveFleetToken(token string) error` (0600).

- [ ] **Step 1: Write the failing test**

Add to `internal/store/store_test.go`:

```go
func TestFleetTokenRoundTrip(t *testing.T) {
	s := NewAt(t.TempDir())
	if err := s.EnsureDir(); err != nil {
		t.Fatal(err)
	}
	got, err := s.LoadFleetToken()
	if err != nil || got != "" {
		t.Fatalf("missing token: %q, %v", got, err)
	}
	if err := s.SaveFleetToken("tok-123"); err != nil {
		t.Fatal(err)
	}
	got, err = s.LoadFleetToken()
	if err != nil || got != "tok-123" {
		t.Fatalf("LoadFleetToken = %q, %v", got, err)
	}
	info, err := os.Stat(s.FleetTokenPath())
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("mode = %o, want 600", info.Mode().Perm())
	}
}
```

(Add `"os"` to the test imports if needed.)

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/store/ -run TestFleetTokenRoundTrip -v`
Expected: FAIL — `undefined: SaveFleetToken`.

- [ ] **Step 3: Write minimal implementation**

Add to `internal/store/store.go`:

```go
import "strings" // if not already imported

// FleetTokenPath is the file holding the minted per-agent fleet token.
func (s *Store) FleetTokenPath() string { return filepath.Join(s.base, "fleet-token") }

// LoadFleetToken reads the minted per-agent token. A missing file yields ("", nil).
func (s *Store) LoadFleetToken() (string, error) {
	b, err := os.ReadFile(s.FleetTokenPath())
	if errors.Is(err, fs.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(b)), nil
}

// SaveFleetToken writes the minted per-agent token (0600).
func (s *Store) SaveFleetToken(token string) error {
	return os.WriteFile(s.FleetTokenPath(), []byte(token), 0o600)
}
```

(`errors` and `io/fs` are already imported in store.go; add `strings` if missing.)

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/store/ -v && gofmt -l internal/store/store.go`
Expected: PASS.

> **Config note:** the "Token required unless already enrolled" check cannot live in `config.validate` (which has no store access). Enforce it at the daemon wiring instead (Task 12): if `sc.Token == ""` and `LoadFleetToken()` is empty, log `fleet: disabled — no token and not enrolled` and skip starting the client. Do NOT add a store dependency to the config package.

- [ ] **Step 5: Commit**

```bash
git add internal/store/
git commit -m "feat(store): persist minted per-agent fleet token

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

## Task 12: Agent client enrollment flow

**Files:**
- Modify: `internal/fleet/client.go` (struct, `connectOnce`, options)
- Modify: `internal/daemon/server.go:254-270` (load fleet-token, pass auth options)
- Test: `internal/fleet/client_test.go`

**Interfaces:**
- Consumes: store token persistence (via a callback to avoid a store import in `fleet`).
- Produces:
  - `fleet.WithAuth(token, enrollToken string, persist func(string) error) Option` — `token` is the per-agent token (may be ""); `enrollToken` is the enroll token (used only when `token` == ""); `persist` is called with the minted token after enrollment.

- [ ] **Step 1: Write the failing test**

Add a test that: starts a TLS server with a known enroll token (via `loadOrInitAuth` in a test helper exposed from `internal/server`, or a hand-built `authStore`); runs a client with `WithAuth("", enrollToken, persist)`; asserts (a) the snapshot reaches the server, (b) `persist` is called with a non-empty token, (c) reconnecting with that token (as `WithAuth(token, "", persist)`) still works. Model the server side on the Task 9 e2e helper. Keep it in `internal/fleet/client_test.go` using an in-process server.

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/fleet/ -run Enroll -v`
Expected: FAIL — `undefined: WithAuth`.

- [ ] **Step 3: Write minimal implementation**

In `internal/fleet/client.go`:

```go
import (
	"google.golang.org/grpc/metadata"
)

type Client struct {
	// ...existing...
	tls        *tls.Config
	token      string               // per-agent token (empty until enrolled)
	enrollTok  string               // enroll token, used only when token == ""
	persistTok func(string) error   // called with the minted token on enrollment
}

// WithAuth configures fleet authentication. token is the per-agent token (empty
// to enroll); enrollToken is used only when token is empty; persist stores the
// minted token after a successful enrollment.
func WithAuth(token, enrollToken string, persist func(string) error) Option {
	return func(c *Client) { c.token, c.enrollTok, c.persistTok = token, enrollToken, persist }
}
```

In `connectOnce`, attach metadata to the stream context before opening the stream, and capture the minted token from the ack:

```go
	// after dialing conn, before NewFleetClient(conn).Connect(ctx):
	if c.token != "" {
		ctx = metadata.AppendToOutgoingContext(ctx, "marshal-token", c.token)
	} else if c.enrollTok != "" {
		ctx = metadata.AppendToOutgoingContext(ctx, "marshal-enroll", c.enrollTok)
	} else {
		return false, errors.New("fleet: no token or enrollment token")
	}
	stream, err := pb.NewFleetClient(conn).Connect(ctx)
	// ...
```

When handling the HelloAck (the existing `ack.GetHelloAck()` block), persist a minted token:

```go
	} else if a := ack.GetHelloAck(); a != nil {
		watermark = a.GetLastMetricTsMs()
		logWM = a.GetLastLogTsMs()
		if mt := a.GetAgentToken(); mt != "" && c.persistTok != nil {
			if err := c.persistTok(mt); err != nil {
				return true, err
			}
			c.token, c.enrollTok = mt, "" // authenticate with the minted token from now on
		}
	}
```

In `internal/daemon/server.go`, load the fleet token and wire the options. Replace the client-construction block (around lines 254-270):

```go
	if sc, err := st.LoadServer(); err == nil && sc != nil {
		name := sc.Name
		if name == "" {
			if h, hErr := os.Hostname(); hErr == nil {
				name = h
			}
		}
		if name == "" {
			name = "unknown"
		}
		fleetTok, _ := st.LoadFleetToken()
		if fleetTok == "" && sc.Token == "" {
			log.Printf("fleet: disabled — no token and not enrolled")
		} else if tlsCfg, tErr := fleetauth.ClientTLS(sc.Fingerprint, sc.CA); tErr != nil {
			log.Printf("fleet: disabled, bad TLS config: %v", tErr)
		} else {
			fc := fleet.New(sc.Address, name, version.String(),
				fleetSnapshot(mgr, sampler),
				fleet.WithTLS(tlsCfg),
				fleet.WithAuth(fleetTok, sc.Token, st.SaveFleetToken),
				fleet.WithMetrics(metricsSince(mdb)),
				fleet.WithLogs(logsSince(reg)),
				fleet.WithCommands(srv.handleFleetCommand))
			go fc.Run(serveCtx)
		}
	}
```

- [ ] **Step 4: Run tests**

Run: `go test ./internal/fleet/ ./internal/daemon/ -race -count=1 -v 2>&1 | tail -25`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/fleet/client.go internal/daemon/server.go
git commit -m "feat(fleet): agent auto-enrollment + per-agent token auth

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

## Task 13: Server Connect honors enrollment + trusts authenticated name

**Files:**
- Modify: `internal/server/server.go:42-102` (`Connect`)
- Test: `internal/server/server_test.go` (e2e enrollment round-trip; reject name spoofing)

**Interfaces:**
- Consumes: `authedAgentName(ctx)`, `isEnrolling(ctx)`, `authStore.enrollAgent`.

- [ ] **Step 1: Write the failing test**

Add an e2e test that drives a raw `Connect` stream:
1. Open `Connect` with `marshal-enroll` metadata; send `Hello{AgentName:"dev-1"}`; assert the `HelloAck.AgentToken` is non-empty.
2. Open a second `Connect` with `marshal-token` = that minted token; send `Hello{AgentName:"anything"}`; assert the server registers state under **`dev-1`** (the authenticated name), not `"anything"`. Verify via `ListFleet` (with admin token) showing agent `dev-1` after a snapshot.

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/server/ -run "Enroll|Connect" -v`
Expected: FAIL — `Connect` still trusts `Hello.AgentName` / does not mint a token.

- [ ] **Step 3: Write minimal implementation**

Rework the `AgentMessage_Hello` case in `Connect` to resolve identity from context:

```go
		case *pb.AgentMessage_Hello:
			ctx := stream.Context()
			ack := &pb.HelloAck{}
			if isEnrolling(ctx) {
				requested := m.Hello.GetAgentName()
				if requested == "" {
					return status.Error(codes.InvalidArgument, "agent_name must not be empty")
				}
				tok, err := s.auth.enrollAgent(requested)
				if err != nil {
					return status.Errorf(codes.AlreadyExists, "enroll %q: %v", requested, err)
				}
				name = requested
				ack.AgentToken = tok
			} else if authed, ok := authedAgentName(ctx); ok {
				name = authed
			} else {
				return status.Error(codes.Unauthenticated, "unauthenticated connect")
			}
			s.reg.Open(name)
			sess = s.broker.register(name, stream.Send)
			var watermark, logWM int64
			if s.stores != nil {
				if st, err := s.stores.get(name); err == nil {
					watermark, _ = st.MaxTs()
				}
			}
			if s.logs != nil {
				if st, err := s.logs.get(name); err == nil {
					logWM, _ = st.MaxTs()
				}
			}
			ack.LastMetricTsMs = watermark
			ack.LastLogTsMs = logWM
			_ = sess.sendMsg(&pb.ServerMessage{Msg: &pb.ServerMessage_HelloAck{HelloAck: ack}})
```

The rest of `Connect` (snapshot/metrics/logs/result cases) is unchanged — they already key off the resolved `name`.

- [ ] **Step 4: Run tests + full suite**

Run: `go test ./internal/server/ -race -v 2>&1 | tail -20 && go test ./... -race -count=1 2>&1 | tail -10`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/server/server.go internal/server/server_test.go
git commit -m "feat(server): enroll on first connect; trust authenticated agent name

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

# Phase 4 — Server CLI + config wiring + docs

## Task 14: `marshal server fingerprint | token | agent` subcommands

**Files:**
- Create: `cmd/marshal/server_auth.go`
- Modify: `cmd/marshal/server.go` (register subcommands under `serverCmd`)
- Test: `cmd/marshal/server_auth_test.go`

**Interfaces:**
- Consumes: `server.LoadOrCreateCert`, and exported helpers from `internal/server` for the auth store. Add thin exported wrappers in `internal/server` so the CLI need not see unexported types:
  - `func Fingerprint(dataDir string) (string, error)`
  - `func RotateToken(dataDir, which string) (string, error)`
  - `func ListAgents(dataDir string) ([]AgentInfo, error)` where `type AgentInfo struct{ Name string; EnrolledAt int64 }`
  - `func RemoveAgent(dataDir, name string) (bool, error)`

- [ ] **Step 1: Write the failing test**

```go
package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestServerFingerprintCmd(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_DATA_HOME", dir) // so defaultServerDataDir resolves under temp
	cmd := serverCmd()
	cmd.SetArgs([]string{"fingerprint"})
	var out bytes.Buffer
	cmd.SetOut(&out)
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	if len(strings.TrimSpace(out.String())) < 32 {
		t.Fatalf("expected a fingerprint, got %q", out.String())
	}
}
```

(If `serverCmd`'s `RunE` requires a listener, ensure subcommands are siblings that do not start the server. Verify `defaultServerDataDir` uses `XDG_DATA_HOME`; it does per `cmd/marshal/server.go:44`.)

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./cmd/marshal/ -run TestServerFingerprintCmd -v`
Expected: FAIL — unknown command "fingerprint".

- [ ] **Step 3: Write minimal implementation**

Add exported wrappers in `internal/server/auth.go` (and `Fingerprint(dataDir)` using `LoadOrCreateCert`):

```go
// FingerprintForDir returns the server cert fingerprint, generating the cert if absent.
func FingerprintForDir(dataDir string) (string, error) {
	_, fp, err := LoadOrCreateCert(dataDir, "", "")
	return fp, err
}

type AgentInfo struct {
	Name       string
	EnrolledAt int64
}

func RotateToken(dataDir, which string) (string, error) {
	a, _, err := loadOrInitAuth(dataDir)
	if err != nil {
		return "", err
	}
	return a.rotate(which)
}

func ListAgents(dataDir string) ([]AgentInfo, error) {
	a, _, err := loadOrInitAuth(dataDir)
	if err != nil {
		return nil, err
	}
	out := make([]AgentInfo, 0)
	for _, la := range a.listAgents() {
		out = append(out, AgentInfo{Name: la.Name, EnrolledAt: la.EnrolledAt})
	}
	return out, nil
}

func RemoveAgent(dataDir, name string) (bool, error) {
	a, _, err := loadOrInitAuth(dataDir)
	if err != nil {
		return false, err
	}
	return a.removeAgent(name), nil
}
```

Create `cmd/marshal/server_auth.go`:

```go
package main

import (
	"fmt"
	"time"

	"github.com/spf13/cobra"

	"marshal/internal/server"
)

func serverFingerprintCmd() *cobra.Command {
	var dataDir string
	cmd := &cobra.Command{
		Use:   "fingerprint",
		Short: "Print the server's TLS certificate fingerprint",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if dataDir == "" {
				dataDir = defaultServerDataDir()
			}
			fp, err := server.FingerprintForDir(dataDir)
			if err != nil {
				return err
			}
			fmt.Fprintln(cmd.OutOrStdout(), fp)
			return nil
		},
	}
	cmd.Flags().StringVar(&dataDir, "data-dir", "", "server data directory")
	return cmd
}

func serverTokenCmd() *cobra.Command {
	var dataDir, rotate string
	cmd := &cobra.Command{
		Use:   "token",
		Short: "Rotate the enroll or admin token",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if dataDir == "" {
				dataDir = defaultServerDataDir()
			}
			if rotate == "" {
				return fmt.Errorf("specify --rotate enroll|admin")
			}
			tok, err := server.RotateToken(dataDir, rotate)
			if err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "new %s token: %s\n", rotate, tok)
			return nil
		},
	}
	cmd.Flags().StringVar(&dataDir, "data-dir", "", "server data directory")
	cmd.Flags().StringVar(&rotate, "rotate", "", "which token to rotate: enroll|admin")
	return cmd
}

func serverAgentCmd() *cobra.Command {
	var dataDir string
	cmd := &cobra.Command{Use: "agent", Short: "Manage enrolled agents"}
	ls := &cobra.Command{
		Use: "ls", Short: "List enrolled agents", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if dataDir == "" {
				dataDir = defaultServerDataDir()
			}
			agents, err := server.ListAgents(dataDir)
			if err != nil {
				return err
			}
			for _, a := range agents {
				fmt.Fprintf(cmd.OutOrStdout(), "%s\tenrolled %s\n", a.Name,
					time.Unix(a.EnrolledAt, 0).Format(time.RFC3339))
			}
			return nil
		},
	}
	rm := &cobra.Command{
		Use: "rm <name>", Short: "Revoke an enrolled agent", Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if dataDir == "" {
				dataDir = defaultServerDataDir()
			}
			ok, err := server.RemoveAgent(dataDir, args[0])
			if err != nil {
				return err
			}
			if !ok {
				return fmt.Errorf("no such agent %q", args[0])
			}
			fmt.Fprintf(cmd.OutOrStdout(), "removed %s\n", args[0])
			return nil
		},
	}
	cmd.PersistentFlags().StringVar(&dataDir, "data-dir", "", "server data directory")
	cmd.AddCommand(ls, rm)
	return cmd
}
```

In `cmd/marshal/server.go` `serverCmd()`, before `return cmd`:

```go
	cmd.AddCommand(serverFingerprintCmd(), serverTokenCmd(), serverAgentCmd())
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./cmd/marshal/ -run "TestServer" -v && gofmt -l cmd/marshal/`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add cmd/marshal/server_auth.go cmd/marshal/server.go internal/server/auth.go cmd/marshal/server_auth_test.go
git commit -m "feat(cli): server fingerprint/token/agent subcommands

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

## Task 15: CLI config file fallback (`~/.config/marshal/cli.yaml`)

**Files:**
- Modify: `cmd/marshal/fleet.go` (`resolveServerAuth` reads the config file last)
- Test: `cmd/marshal/fleet_test.go`

**Interfaces:**
- Produces: `resolveServerAuth` resolution order flag → env → `~/.config/marshal/cli.yaml`.

- [ ] **Step 1: Write the failing test**

```go
func TestResolveServerAuthFromConfigFile(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	if err := os.MkdirAll(filepath.Join(dir, "marshal"), 0o700); err != nil {
		t.Fatal(err)
	}
	body := "server: h:1234\ntoken: tok-cfg\nfingerprint: fp-cfg\n"
	if err := os.WriteFile(filepath.Join(dir, "marshal", "cli.yaml"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("MARSHAL_SERVER", "")
	t.Setenv("MARSHAL_TOKEN", "")
	t.Setenv("MARSHAL_FINGERPRINT", "")
	addr, fp, tok := resolveServerAuth("", "", "")
	if addr != "h:1234" || fp != "fp-cfg" || tok != "tok-cfg" {
		t.Fatalf("got addr=%q fp=%q tok=%q", addr, fp, tok)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./cmd/marshal/ -run TestResolveServerAuthFromConfigFile -v`
Expected: FAIL — config file not consulted.

- [ ] **Step 3: Write minimal implementation**

Add a small loader and fold it into `resolveServerAuth` as the lowest-priority source:

```go
type cliConfig struct {
	Server      string `yaml:"server"`
	Token       string `yaml:"token"`
	Fingerprint string `yaml:"fingerprint"`
}

func loadCLIConfig() cliConfig {
	base := os.Getenv("XDG_CONFIG_HOME")
	if base == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return cliConfig{}
		}
		base = filepath.Join(home, ".config")
	}
	b, err := os.ReadFile(filepath.Join(base, "marshal", "cli.yaml"))
	if err != nil {
		return cliConfig{}
	}
	var c cliConfig
	_ = yaml.Unmarshal(b, &c)
	return c
}

func resolveServerAuth(serverFlag, fpFlag, tokenFlag string) (addr, fingerprint, token string) {
	cfg := loadCLIConfig()
	addr = firstNonEmpty(serverFlag, os.Getenv("MARSHAL_SERVER"), cfg.Server, "localhost:9000")
	fingerprint = firstNonEmpty(fpFlag, os.Getenv("MARSHAL_FINGERPRINT"), cfg.Fingerprint)
	token = firstNonEmpty(tokenFlag, os.Getenv("MARSHAL_TOKEN"), cfg.Token)
	return
}
```

Add `firstNonEmpty` if not already present:

```go
func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}
```

Import `"path/filepath"` and the project's YAML package (`gopkg.in/yaml.v3` — match what `internal/config` uses). Note `resolveServer` is now only used by `resolveServerAuth`/tests; keep or inline it as appropriate.

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./cmd/marshal/ -v 2>&1 | tail -20 && gofmt -l cmd/marshal/`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add cmd/marshal/fleet.go cmd/marshal/fleet_test.go
git commit -m "feat(cli): cli.yaml fallback for server/token/fingerprint

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

## Task 16: Full-suite gate, smoke test, and handoff doc

**Files:**
- Create: `docs/handoffs/2026-06-17-m10-auth-tls.md`

- [ ] **Step 1: Green gate**

Run:
```bash
go build ./... && go test ./... -race -count=1 && gofmt -l . && go vet ./...
```
Expected: tests pass; `gofmt -l .` prints nothing; vet clean. Fix anything that fails.

- [ ] **Step 2: Manual smoke test**

```bash
export XDG_DATA_HOME=/tmp/m10smoke && rm -rf "$XDG_DATA_HOME"
go build -o marshal ./cmd/marshal
# Start server; capture the printed enroll token, admin token, and fingerprint.
./marshal server --listen :9000 &
# Build an app.yaml with server:{address: localhost:9000, name: dev-1, token: <ENROLL>, fingerprint: <FP>}.
./marshal start app.yaml
# Agent enrolls; confirm fleet-token exists:
ls -l "$XDG_DATA_HOME/marshal/fleet-token"
# Operator commands with the admin token:
./marshal fleet ps --server localhost:9000 --token <ADMIN> --fingerprint <FP>
./marshal fleet restart dev-1 <app> --server localhost:9000 --token <ADMIN> --fingerprint <FP>
# Negative checks:
./marshal fleet ps --server localhost:9000 --fingerprint <FP>            # no token -> Unauthenticated
./marshal fleet ps --server localhost:9000 --token <ADMIN>               # no fingerprint -> TLS pin error
./marshal server agent ls   # lists dev-1
```
Expected: enrolled agent appears; commands succeed with the admin token; both negative checks fail as described. Record actual output in the handoff.

- [ ] **Step 3: Write the handoff**

Create `docs/handoffs/2026-06-17-m10-auth-tls.md` per the CLAUDE.md handoff convention: current state, what changed + key decisions (link the spec), build/run/test, deferred items (dashboard sessions, read/admin token tiers, per-agent token rotation, command audit log), and the concrete next step (review + merge `m10-auth-tls`; then M11 or the dashboard).

- [ ] **Step 4: Commit**

```bash
git add docs/handoffs/2026-06-17-m10-auth-tls.md
git commit -m "docs: M10 auth/TLS handoff

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

## Self-review (completed during planning)

**Spec coverage:** §3.1 TLS+pin → Tasks 1,2,4,5; §3.2 enrollment → Tasks 7,10,11,12,13; §3.3 admin token → Tasks 7,8,9; §3.4 authorization interceptor → Tasks 8,9; §4 wire (`HelloAck.agent_token`, metadata) → Tasks 10,12,8,9; §5 config/disk → Tasks 3,7,11; §5 CLI resolution → Tasks 5,9,15; §6 CLI subcommands → Task 14; §7 no-plaintext → Tasks 4,5 (insecure removed); §8 tests → every task + Task 16 e2e/smoke. No gaps.

**Type consistency:** `authStore` methods (`verifyAdmin/verifyEnroll/enrollAgent/authAgent/removeAgent/listAgents/rotate`) are defined in Task 7 and consumed with matching signatures in Tasks 8,9,13,14. `Serve(...cert, auth)` / `NewServer(...auth)` arity is introduced in Task 4 (cert) then Task 9 (auth) — **the implementer must update all call sites at each arity change**; flagged in both tasks. `fleet.WithTLS`/`WithAuth` signatures match between client (Tasks 5,12) and daemon wiring (Tasks 5,12). `HelloAck.GetAgentToken()` (Task 10) used in Tasks 12,13.

**Known sequencing risk:** `Serve`/`NewServer` change arity twice (Task 4 adds `cert`, Task 9 adds `auth`). Each task explicitly says to update all callers; run `rg -n "server.Serve\(|NewServer\(" ` after each to catch stragglers.
