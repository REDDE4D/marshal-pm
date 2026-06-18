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
