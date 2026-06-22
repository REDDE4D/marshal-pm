package notify

import (
	"context"
	"log"
	"sync"
	"time"
)

// StoreReader is the read surface the dispatcher needs (the *Store satisfies it).
type StoreReader interface {
	Rules() []Rule
	Channels() []Channel
	ChannelSecrets(name string) (map[string]string, bool, error)
	Settings() Settings
}

// BuildFunc constructs a Sender for a channel + its decrypted secrets.
type BuildFunc func(c Channel, secrets map[string]string) (Sender, error)

// Dispatcher gates events by cooldown, matches rules, and fans out to channels.
type Dispatcher struct {
	store    StoreReader
	build    BuildFunc
	now      func() time.Time
	syncMode bool
	mu       sync.Mutex
	last     map[string]cooldownEntry
}

// cooldownEntry records when a (agent,process,type) key last fired and its type,
// so the prune sweep can apply the type's own cooldown without re-parsing the key.
type cooldownEntry struct {
	at  time.Time
	typ EventType
}

// DispatchOption configures a Dispatcher.
type DispatchOption func(*Dispatcher)

// WithClock overrides the clock (tests).
func WithClock(fn func() time.Time) DispatchOption { return func(d *Dispatcher) { d.now = fn } }

// WithSyncDelivery delivers inline instead of in goroutines (tests).
func WithSyncDelivery() DispatchOption { return func(d *Dispatcher) { d.syncMode = true } }

// NewDispatcher builds a dispatcher.
func NewDispatcher(store StoreReader, build BuildFunc, opts ...DispatchOption) *Dispatcher {
	d := &Dispatcher{
		store: store,
		build: build,
		now:   time.Now,
		last:  map[string]cooldownEntry{},
	}
	for _, o := range opts {
		o(d)
	}
	return d
}

// Emit gates the event by cooldown, then fans out to matching channels.
func (d *Dispatcher) Emit(e Event) {
	if e.Type == EventRecovered && d.store.Settings().SuppressRecovery {
		return
	}
	if !d.allow(e) {
		return
	}
	targets := d.matchChannels(e)
	if len(targets) == 0 {
		return
	}
	msg := render(e)
	for _, c := range targets {
		if d.syncMode {
			d.deliver(c, msg)
		} else {
			go d.deliver(c, msg)
		}
	}
}

// allow records and checks the per-(agent,process,type) cooldown, then prunes
// entries that have outlived their own cooldown (they can never gate again).
func (d *Dispatcher) allow(e Event) bool {
	key := e.Agent + "\x00" + e.Process + "\x00" + string(e.Type)
	s := d.store.Settings()
	now := d.now()
	d.mu.Lock()
	defer d.mu.Unlock()
	if last, ok := d.last[key]; ok && now.Sub(last.at) < s.cooldownFor(e.Type) {
		return false
	}
	d.last[key] = cooldownEntry{at: now, typ: e.Type}
	d.pruneLocked(s, now)
	return true
}

// pruneLocked drops entries whose age has reached their type's cooldown. Caller
// holds d.mu. For a fixed or lowered cooldown, an entry past its cooldown always
// allows the next event of that key, so removing it changes no observable behavior.
// (If the cooldown is raised at runtime, a swept key may permit one early re-fire —
// benign: it can only over-notify, never suppress an alert.) This bounds the map to
// distinct keys seen within their cooldown window.
func (d *Dispatcher) pruneLocked(s Settings, now time.Time) {
	for k, e := range d.last {
		if now.Sub(e.at) >= s.cooldownFor(e.typ) {
			delete(d.last, k)
		}
	}
}

// matchChannels returns the deduplicated, enabled channels for an event.
func (d *Dispatcher) matchChannels(e Event) []Channel {
	byName := map[string]Channel{}
	for _, c := range d.store.Channels() {
		byName[c.Name] = c
	}
	seen := map[string]bool{}
	var out []Channel
	for _, r := range d.store.Rules() {
		if !r.Matches(e) {
			continue
		}
		for _, name := range r.Channels {
			if seen[name] {
				continue
			}
			c, ok := byName[name]
			if !ok || !c.Enabled {
				continue
			}
			seen[name] = true
			out = append(out, c)
		}
	}
	return out
}

func (d *Dispatcher) deliver(c Channel, msg Message) {
	secrets, _, err := d.store.ChannelSecrets(c.Name)
	if err != nil {
		log.Printf("notify: channel %q: secret: %v", c.Name, err)
		return
	}
	sender, err := d.build(c, secrets)
	if err != nil {
		log.Printf("notify: channel %q: build: %v", c.Name, err)
		return
	}
	const attempts = 3
	for i := 0; i < attempts; i++ {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		err = sender.Send(ctx, msg)
		cancel()
		if err == nil {
			return
		}
		time.Sleep(time.Duration(i+1) * 200 * time.Millisecond)
	}
	log.Printf("notify: channel %q send failed after %d attempts: %v", c.Name, attempts, err)
}
