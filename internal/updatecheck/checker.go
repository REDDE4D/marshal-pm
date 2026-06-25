package updatecheck

import (
	"context"
	"net/http"
	"sync"
	"time"
)

// DefaultInterval is how often the background poller re-checks for a release.
const DefaultInterval = 24 * time.Hour

// Checker periodically resolves the latest release and caches the comparison
// against the running version. It is safe for concurrent use. A disabled checker
// performs no network requests and always reports an empty snapshot.
type Checker struct {
	current     string
	releasesURL string
	client      *http.Client
	enabled     bool
	now         func() time.Time

	mu   sync.Mutex
	last Result
}

// Option configures a Checker.
type Option func(*Checker)

// WithReleasesURL overrides the GitHub releases endpoint (used by tests).
func WithReleasesURL(u string) Option { return func(c *Checker) { c.releasesURL = u } }

// WithHTTPClient overrides the HTTP client.
func WithHTTPClient(h *http.Client) Option { return func(c *Checker) { c.client = h } }

// WithEnabled toggles the checker (false disables all network activity).
func WithEnabled(b bool) Option { return func(c *Checker) { c.enabled = b } }

// WithClock overrides time.Now (used by tests).
func WithClock(fn func() time.Time) Option { return func(c *Checker) { c.now = fn } }

// New builds a Checker for the given running version. It is enabled by default.
func New(current string, opts ...Option) *Checker {
	c := &Checker{
		current:     current,
		releasesURL: DefaultReleasesURL,
		client:      &http.Client{Timeout: 10 * time.Second},
		enabled:     true,
		now:         time.Now,
	}
	for _, o := range opts {
		o(c)
	}
	return c
}

// Enabled reports whether the checker performs update checks.
func (c *Checker) Enabled() bool { return c.enabled }

// Snapshot returns the most recent check result (zero-valued until the first
// successful refresh, or always zero-valued when disabled).
func (c *Checker) Snapshot() Result {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.last
}

// refresh performs one check and updates the cached snapshot. It is a no-op when
// disabled. A fetch error is returned and leaves the previous snapshot intact.
func (c *Checker) refresh(ctx context.Context) error {
	if !c.enabled {
		return nil
	}
	latest, err := fetchLatest(ctx, c.client, c.releasesURL)
	if err != nil {
		return err
	}
	c.mu.Lock()
	c.last = Result{
		Current:   c.current,
		Latest:    latest,
		Outdated:  Outdated(c.current, latest),
		CheckedAt: c.now(),
	}
	c.mu.Unlock()
	return nil
}

// Run refreshes once immediately, then every DefaultInterval until ctx is done.
// Fetch errors are swallowed (the check is best-effort and must never disrupt
// the server); the previous snapshot is retained across a failed refresh.
func (c *Checker) Run(ctx context.Context) {
	if !c.enabled {
		return
	}
	_ = c.refresh(ctx)
	t := time.NewTicker(DefaultInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			_ = c.refresh(ctx)
		}
	}
}
