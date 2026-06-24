package notify

import (
	"context"
	"sync"
	"testing"
	"time"
)

type fakeSender struct {
	mu     sync.Mutex
	sent   []Message
	chName string
}

func (f *fakeSender) Send(_ context.Context, m Message) error {
	f.mu.Lock()
	f.sent = append(f.sent, m)
	f.mu.Unlock()
	return nil
}

type fakeStore struct {
	rules    []Rule
	channels []Channel
	settings Settings
}

func (s *fakeStore) Rules() []Rule       { return s.rules }
func (s *fakeStore) Channels() []Channel { return s.channels }
func (s *fakeStore) Settings() Settings  { return s.settings }
func (s *fakeStore) ChannelSecrets(string) (map[string]string, bool, error) {
	return map[string]string{}, true, nil
}

func newTestDispatcher(t *testing.T, st *fakeStore, clock func() time.Time) (*Dispatcher, map[string]*fakeSender) {
	t.Helper()
	senders := map[string]*fakeSender{}
	build := func(c Channel, _ map[string]string) (Sender, error) {
		if existing, ok := senders[c.Name]; ok {
			return existing, nil
		}
		fs := &fakeSender{chName: c.Name}
		senders[c.Name] = fs
		return fs, nil
	}
	d := NewDispatcher(st, build, WithSyncDelivery(), WithClock(clock))
	return d, senders
}

func TestDispatcherFanOutToMatchingChannels(t *testing.T) {
	st := &fakeStore{
		channels: []Channel{{Name: "tg", Type: "telegram", Enabled: true}, {Name: "wh", Type: "webhook", Enabled: true}},
		rules:    []Rule{{Name: "crashes", Enabled: true, Events: []EventType{EventCrash}, Channels: []string{"tg"}}},
		settings: Settings{CooldownSeconds: 300},
	}
	now := time.Unix(1000, 0)
	d, senders := newTestDispatcher(t, st, func() time.Time { return now })
	d.Emit(Event{Type: EventCrash, Agent: "dev-1", Process: "api"})
	if len(senders["tg"].sent) != 1 {
		t.Fatalf("tg should get 1, got %d", len(senders["tg"].sent))
	}
	if s := senders["wh"]; s != nil && len(s.sent) != 0 {
		t.Fatalf("wh should not fire (no matching rule)")
	}
}

func TestDispatcherCooldownSuppressesRepeat(t *testing.T) {
	st := &fakeStore{
		channels: []Channel{{Name: "tg", Type: "telegram", Enabled: true}},
		rules:    []Rule{{Name: "all", Enabled: true, Channels: []string{"tg"}}},
		settings: Settings{CooldownSeconds: 300},
	}
	cur := time.Unix(1000, 0)
	d, senders := newTestDispatcher(t, st, func() time.Time { return cur })
	ev := Event{Type: EventCrash, Agent: "dev-1", Process: "api"}
	d.Emit(ev)
	cur = cur.Add(60 * time.Second) // within cooldown
	d.Emit(ev)
	if len(senders["tg"].sent) != 1 {
		t.Fatalf("cooldown should suppress, got %d sends", len(senders["tg"].sent))
	}
	cur = cur.Add(300 * time.Second) // past cooldown
	d.Emit(ev)
	if len(senders["tg"].sent) != 2 {
		t.Fatalf("should fire after cooldown, got %d", len(senders["tg"].sent))
	}
}

func TestDispatcherDedupAcrossRules(t *testing.T) {
	st := &fakeStore{
		channels: []Channel{{Name: "tg", Type: "telegram", Enabled: true}},
		rules: []Rule{
			{Name: "r1", Enabled: true, Channels: []string{"tg"}},
			{Name: "r2", Enabled: true, Events: []EventType{EventCrash}, Channels: []string{"tg"}},
		},
		settings: Settings{CooldownSeconds: 300},
	}
	now := time.Unix(1000, 0)
	d, senders := newTestDispatcher(t, st, func() time.Time { return now })
	d.Emit(Event{Type: EventCrash, Agent: "dev-1", Process: "api"})
	if len(senders["tg"].sent) != 1 {
		t.Fatalf("two matching rules → one send, got %d", len(senders["tg"].sent))
	}
}

func TestDispatcherSkipsDisabledChannel(t *testing.T) {
	st := &fakeStore{
		channels: []Channel{{Name: "tg", Type: "telegram", Enabled: false}},
		rules:    []Rule{{Name: "all", Enabled: true, Channels: []string{"tg"}}},
		settings: Settings{CooldownSeconds: 300},
	}
	now := time.Unix(1000, 0)
	d, senders := newTestDispatcher(t, st, func() time.Time { return now })
	d.Emit(Event{Type: EventCrash, Agent: "dev-1", Process: "api"})
	if s := senders["tg"]; s != nil && len(s.sent) != 0 {
		t.Fatal("disabled channel must not fire")
	}
}

func TestDispatcherSuppressesRecovery(t *testing.T) {
	st := &fakeStore{
		channels: []Channel{{Name: "tg", Type: "telegram", Enabled: true}},
		rules:    []Rule{{Name: "all", Enabled: true, Channels: []string{"tg"}}},
		settings: Settings{CooldownSeconds: 300, SuppressRecovery: true},
	}
	now := time.Unix(1000, 0)
	d, senders := newTestDispatcher(t, st, func() time.Time { return now })
	d.Emit(Event{Type: EventRecovered, Agent: "dev-1", Process: "api"})
	if s := senders["tg"]; s != nil && len(s.sent) != 0 {
		t.Fatalf("recovered must be suppressed, got %d", len(s.sent))
	}
	d.Emit(Event{Type: EventCrash, Agent: "dev-1", Process: "api"})
	if len(senders["tg"].sent) != 1 {
		t.Fatalf("crash should still deliver, got %d", len(senders["tg"].sent))
	}
}

func TestDispatcherDeliversRecoveryWhenEnabled(t *testing.T) {
	st := &fakeStore{
		channels: []Channel{{Name: "tg", Type: "telegram", Enabled: true}},
		rules:    []Rule{{Name: "all", Enabled: true, Channels: []string{"tg"}}},
		settings: Settings{CooldownSeconds: 300, SuppressRecovery: false},
	}
	now := time.Unix(1000, 0)
	d, senders := newTestDispatcher(t, st, func() time.Time { return now })
	d.Emit(Event{Type: EventRecovered, Agent: "dev-1", Process: "api"})
	if len(senders["tg"].sent) != 1 {
		t.Fatalf("recovered should deliver when enabled, got %d", len(senders["tg"].sent))
	}
}

func TestDispatcherPerTypeCooldownOverride(t *testing.T) {
	st := &fakeStore{
		channels: []Channel{{Name: "tg", Type: "telegram", Enabled: true}},
		rules:    []Rule{{Name: "all", Enabled: true, Channels: []string{"tg"}}},
		settings: Settings{CooldownSeconds: 300, CooldownOverrides: map[EventType]int{EventRecovered: 30}},
	}
	cur := time.Unix(1000, 0)
	d, senders := newTestDispatcher(t, st, func() time.Time { return cur })
	rec := Event{Type: EventRecovered, Agent: "dev-1", Process: "api"}
	d.Emit(rec)
	cur = cur.Add(60 * time.Second) // past the 30s override, within the 300s global
	d.Emit(rec)
	if n := len(senders["tg"].sent); n != 2 {
		t.Fatalf("recovered should re-fire after its 30s override, got %d", n)
	}
	// A different type still uses the 300s global: a repeat within 60s is suppressed.
	crash := Event{Type: EventCrash, Agent: "dev-1", Process: "api"}
	d.Emit(crash)
	cur = cur.Add(60 * time.Second)
	d.Emit(crash)
	if n := len(senders["tg"].sent); n != 3 {
		t.Fatalf("crash should obey the 300s global (one extra send), got %d total", n)
	}
}

func TestDispatcherZeroOverrideDisablesCooldown(t *testing.T) {
	st := &fakeStore{
		channels: []Channel{{Name: "tg", Type: "telegram", Enabled: true}},
		rules:    []Rule{{Name: "all", Enabled: true, Channels: []string{"tg"}}},
		settings: Settings{CooldownSeconds: 300, CooldownOverrides: map[EventType]int{EventCrash: 0}},
	}
	now := time.Unix(1000, 0)
	d, senders := newTestDispatcher(t, st, func() time.Time { return now })
	ev := Event{Type: EventCrash, Agent: "dev-1", Process: "api"}
	d.Emit(ev)
	d.Emit(ev) // same instant; zero cooldown ⇒ both allowed
	if n := len(senders["tg"].sent); n != 2 {
		t.Fatalf("zero override disables cooldown, want 2 sends, got %d", n)
	}
}

func TestDispatcherPrunesExpiredEntries(t *testing.T) {
	st := &fakeStore{
		channels: []Channel{{Name: "tg", Type: "telegram", Enabled: true}},
		rules:    []Rule{{Name: "all", Enabled: true, Channels: []string{"tg"}}},
		settings: Settings{CooldownSeconds: 300},
	}
	cur := time.Unix(1000, 0)
	d, _ := newTestDispatcher(t, st, func() time.Time { return cur })
	d.Emit(Event{Type: EventCrash, Agent: "dev-1", Process: "api"})
	d.Emit(Event{Type: EventCrash, Agent: "dev-2", Process: "api"}) // within cooldown: both retained
	if n := len(d.last); n != 2 {
		t.Fatalf("two live keys within cooldown, want 2, got %d", n)
	}
	cur = cur.Add(301 * time.Second) // both now past the 300s cooldown
	d.Emit(Event{Type: EventCrash, Agent: "dev-3", Process: "api"})
	if n := len(d.last); n != 1 {
		t.Fatalf("expired entries should be pruned, want 1 (only dev-3), got %d", n)
	}
}
