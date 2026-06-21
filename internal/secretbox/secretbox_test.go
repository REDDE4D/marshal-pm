package secretbox

import "testing"

func TestSealOpenRoundTrip(t *testing.T) {
	var key [32]byte
	for i := range key {
		key[i] = byte(i)
	}
	b := FromKey(key)
	nonce, ct, err := b.Seal([]byte("hunter2"))
	if err != nil {
		t.Fatal(err)
	}
	if ct == "" || nonce == "" {
		t.Fatal("empty seal output")
	}
	pt, err := b.Open(nonce, ct)
	if err != nil {
		t.Fatal(err)
	}
	if string(pt) != "hunter2" {
		t.Fatalf("got %q", pt)
	}
}

func TestOpenWrongKeyFails(t *testing.T) {
	var k1, k2 [32]byte
	k2[0] = 1
	nonce, ct, _ := FromKey(k1).Seal([]byte("secret"))
	if _, err := FromKey(k2).Open(nonce, ct); err == nil {
		t.Fatal("expected decrypt failure with wrong key")
	}
}

func TestLoadGeneratesMasterKey(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("MARSHAL_MASTER_KEY", "")
	b1, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	nonce, ct, _ := b1.Seal([]byte("x"))
	b2, err := Load(dir) // reloads the same generated master.key
	if err != nil {
		t.Fatal(err)
	}
	pt, err := b2.Open(nonce, ct)
	if err != nil || string(pt) != "x" {
		t.Fatalf("reload mismatch: %v %q", err, pt)
	}
}
