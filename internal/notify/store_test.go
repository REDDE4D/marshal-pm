package notify

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/REDDE4D/marshal-pm/internal/secretbox"
)

func testStore(t *testing.T) (*Store, string) {
	t.Helper()
	dir := t.TempDir()
	var key [32]byte
	key[0] = 7
	s, err := Open(dir, secretbox.FromKey(key))
	if err != nil {
		t.Fatal(err)
	}
	return s, dir
}

func TestPutGetChannelSecretsSealed(t *testing.T) {
	s, dir := testStore(t)
	ch := Channel{Name: "tg", Type: "telegram", Enabled: true, Config: map[string]string{"chat_id": "42"}}
	if err := s.PutChannel(ch, map[string]string{"bot_token": "SECRET123"}); err != nil {
		t.Fatal(err)
	}
	// metadata view never carries the secret
	got := s.Channels()
	if len(got) != 1 || got[0].Name != "tg" || got[0].Config["chat_id"] != "42" {
		t.Fatalf("channels view wrong: %+v", got)
	}
	// secret retrievable for sending
	sec, ok, err := s.ChannelSecrets("tg")
	if err != nil || !ok || sec["bot_token"] != "SECRET123" {
		t.Fatalf("secret round-trip failed: %v %v %v", sec, ok, err)
	}
	// on-disk file has no plaintext secret
	raw, _ := os.ReadFile(filepath.Join(dir, "notifications.json"))
	if strings.Contains(string(raw), "SECRET123") {
		t.Fatal("plaintext secret leaked to disk")
	}
	if !s.HasSecret("tg") {
		t.Fatal("HasSecret should be true")
	}
}

func TestPutChannelEmptySecretKeepsOld(t *testing.T) {
	s, _ := testStore(t)
	_ = s.PutChannel(Channel{Name: "wh", Type: "webhook"}, map[string]string{"hmac": "k1"})
	_ = s.PutChannel(Channel{Name: "wh", Type: "webhook", Enabled: true}, nil) // update, no new secret
	sec, ok, _ := s.ChannelSecrets("wh")
	if !ok || sec["hmac"] != "k1" {
		t.Fatalf("expected kept secret, got %v", sec)
	}
}

func TestRulesAndSettingsPersist(t *testing.T) {
	s, dir := testStore(t)
	_ = s.PutRule(Rule{Name: "r1", Enabled: true, Events: []EventType{EventCrash}, Channels: []string{"wh"}})
	_ = s.SetSettings(Settings{CooldownSeconds: 120})
	var key [32]byte
	key[0] = 7
	s2, err := Open(dir, secretbox.FromKey(key)) // reload
	if err != nil {
		t.Fatal(err)
	}
	if rs := s2.Rules(); len(rs) != 1 || rs[0].Name != "r1" {
		t.Fatalf("rules not persisted: %+v", rs)
	}
	if s2.Settings().CooldownSeconds != 120 {
		t.Fatalf("settings not persisted: %+v", s2.Settings())
	}
}

func TestDefaultCooldown(t *testing.T) {
	s, _ := testStore(t)
	if s.Settings().CooldownSeconds != 300 {
		t.Fatalf("want default 300, got %d", s.Settings().CooldownSeconds)
	}
}

func TestPutChannelConfigNotAliased(t *testing.T) {
	s, _ := testStore(t)
	// Put a channel with a Config map the caller retains a reference to.
	callerCfg := map[string]string{"chat_id": "99"}
	if err := s.PutChannel(Channel{Name: "alias-test", Type: "telegram", Enabled: true, Config: callerCfg}, nil); err != nil {
		t.Fatal(err)
	}

	// Mutate the caller's original map — must not affect the store.
	callerCfg["chat_id"] = "MUTATED-BY-CALLER"

	// Mutate the Config of a Channel returned by Channels() — must not affect the store.
	got := s.Channels()
	if len(got) != 1 {
		t.Fatalf("expected 1 channel, got %d", len(got))
	}
	got[0].Config["chat_id"] = "MUTATED-BY-RETURNED-VIEW"

	// A fresh read from the store must still see the original value.
	fresh := s.Channels()
	if len(fresh) != 1 {
		t.Fatalf("expected 1 channel after re-read, got %d", len(fresh))
	}
	if fresh[0].Config["chat_id"] != "99" {
		t.Fatalf("aliasing detected: store Config was mutated, got %q", fresh[0].Config["chat_id"])
	}
}

func TestDefaultRecoveryEnabled(t *testing.T) {
	s, _ := testStore(t)
	if s.Settings().SuppressRecovery {
		t.Fatal("recovery should default ON (SuppressRecovery=false)")
	}
}

func TestSuppressRecoveryPersists(t *testing.T) {
	s, dir := testStore(t)
	if err := s.SetSettings(Settings{CooldownSeconds: 120, SuppressRecovery: true}); err != nil {
		t.Fatal(err)
	}
	var key [32]byte
	key[0] = 7
	s2, err := Open(dir, secretbox.FromKey(key))
	if err != nil {
		t.Fatal(err)
	}
	if !s2.Settings().SuppressRecovery {
		t.Fatalf("suppress_recovery not persisted: %+v", s2.Settings())
	}
}

func TestLegacyConfigRecoveryEnabled(t *testing.T) {
	dir := t.TempDir()
	// A notifications.json predating the field: no suppress_recovery key.
	legacy := `{"channels":{},"rules":{},"settings":{"cooldown_seconds":300}}`
	if err := os.WriteFile(filepath.Join(dir, "notifications.json"), []byte(legacy), 0o600); err != nil {
		t.Fatal(err)
	}
	var key [32]byte
	key[0] = 7
	s, err := Open(dir, secretbox.FromKey(key))
	if err != nil {
		t.Fatal(err)
	}
	if s.Settings().SuppressRecovery {
		t.Fatal("legacy config must default to recovery ON")
	}
}

func TestCooldownOverridesPersist(t *testing.T) {
	s, dir := testStore(t)
	if err := s.SetSettings(Settings{
		CooldownSeconds:   120,
		CooldownOverrides: map[EventType]int{EventRecovered: 600},
	}); err != nil {
		t.Fatal(err)
	}
	var key [32]byte
	key[0] = 7
	s2, err := Open(dir, secretbox.FromKey(key))
	if err != nil {
		t.Fatal(err)
	}
	if got := s2.Settings().CooldownOverrides[EventRecovered]; got != 600 {
		t.Fatalf("override not persisted: %+v", s2.Settings())
	}
}

func TestNoCooldownOverridesStaysNil(t *testing.T) {
	s, dir := testStore(t)
	if err := s.SetSettings(Settings{CooldownSeconds: 120}); err != nil {
		t.Fatal(err)
	}
	var key [32]byte
	key[0] = 7
	s2, err := Open(dir, secretbox.FromKey(key))
	if err != nil {
		t.Fatal(err)
	}
	if s2.Settings().CooldownOverrides != nil {
		t.Fatalf("expected nil overrides, got %+v", s2.Settings().CooldownOverrides)
	}
}

func TestLegacyConfigNoOverrides(t *testing.T) {
	dir := t.TempDir()
	// A notifications.json predating the field: no cooldown_overrides key.
	legacy := `{"channels":{},"rules":{},"settings":{"cooldown_seconds":300}}`
	if err := os.WriteFile(filepath.Join(dir, "notifications.json"), []byte(legacy), 0o600); err != nil {
		t.Fatal(err)
	}
	var key [32]byte
	key[0] = 7
	s, err := Open(dir, secretbox.FromKey(key))
	if err != nil {
		t.Fatal(err)
	}
	if s.Settings().CooldownOverrides != nil {
		t.Fatal("legacy config must load with nil overrides")
	}
	if s.Settings().CooldownSeconds != 300 {
		t.Fatalf("legacy global cooldown lost: %d", s.Settings().CooldownSeconds)
	}
}
