// Package fleet holds the agent-side client that streams local process state to
// the Marshal central server. It is additive: with no server configured it never
// runs, and a server outage never affects local supervision.
package fleet

import (
	"context"
	"crypto/tls"
	"errors"
	"log"
	"sync"
	"time"

	"marshal/internal/pb"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/metadata"
)

// SnapshotFunc returns the agent's current process state.
type SnapshotFunc func() []*pb.ProcInfo

// MetricsFunc returns local metric rows strictly newer than sinceTsMs.
type MetricsFunc func(sinceTsMs int64) []*pb.MetricSample

// LogsFunc returns captured log lines strictly newer than sinceTsMs.
type LogsFunc func(sinceTsMs int64) []*pb.LogShipLine

// CommandFunc executes a control command and returns its result.
type CommandFunc func(*pb.Command) *pb.ControlResult

// Client maintains one outbound stream to the central server.
type Client struct {
	addr       string
	name       string
	version    string
	snapshot   SnapshotFunc
	metrics    MetricsFunc
	logs       LogsFunc
	commands   CommandFunc
	interval   time.Duration
	minBO      time.Duration
	maxBO      time.Duration
	tls        *tls.Config
	token      string             // per-agent token (empty until enrolled)
	enrollTok  string             // enroll token, used only when token == ""
	persistTok func(string) error // called with the minted token on enrollment
}

// Option configures a Client.
type Option func(*Client)

// WithInterval sets the snapshot push cadence (also the liveness heartbeat).
func WithInterval(d time.Duration) Option { return func(c *Client) { c.interval = d } }

// WithBackoff sets the reconnect backoff bounds.
func WithBackoff(min, max time.Duration) Option {
	return func(c *Client) { c.minBO, c.maxBO = min, max }
}

// WithMetrics enables upstream metric shipping sourced from fn.
func WithMetrics(fn MetricsFunc) Option { return func(c *Client) { c.metrics = fn } }

// WithLogs enables upstream log shipping sourced from fn.
func WithLogs(fn LogsFunc) Option { return func(c *Client) { c.logs = fn } }

// WithCommands enables down-stream command handling sourced from fn.
func WithCommands(fn CommandFunc) Option { return func(c *Client) { c.commands = fn } }

// WithTLS sets the client TLS config (pinned fingerprint or CA). Required in
// fleet mode; there is no insecure fallback.
func WithTLS(cfg *tls.Config) Option { return func(c *Client) { c.tls = cfg } }

// WithAuth configures fleet authentication. token is the per-agent token (empty
// to enroll); enrollToken is used only when token is empty; persist stores the
// minted token after a successful enrollment.
func WithAuth(token, enrollToken string, persist func(string) error) Option {
	return func(c *Client) { c.token, c.enrollTok, c.persistTok = token, enrollToken, persist }
}

// New builds a fleet client. snap must be non-nil.
func New(addr, name, version string, snap SnapshotFunc, opts ...Option) *Client {
	c := &Client{
		addr: addr, name: name, version: version, snapshot: snap,
		interval: 2 * time.Second, minBO: time.Second, maxBO: 30 * time.Second,
	}
	for _, o := range opts {
		o(c)
	}
	return c
}

// Run maintains the connection until ctx is canceled, reconnecting with
// exponential backoff. Backoff resets after a stream is successfully established.
func (c *Client) Run(ctx context.Context) {
	backoff := c.minBO
	for {
		if ctx.Err() != nil {
			return
		}
		established, err := c.connectOnce(ctx)
		if ctx.Err() != nil {
			return
		}
		if err != nil && !errors.Is(err, context.Canceled) {
			log.Printf("fleet: connection to %s ended: %v", c.addr, err)
		}
		if established {
			backoff = c.minBO
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}
		if backoff *= 2; backoff > c.maxBO {
			backoff = c.maxBO
		}
	}
}

// connectOnce dials, sends Hello, receives the HelloAck to seed the metric
// watermark, then pushes snapshots and metrics until the stream errors or ctx
// is canceled. The bool reports whether the stream was established.
func (c *Client) connectOnce(ctx context.Context) (bool, error) {
	if c.tls == nil {
		return false, errors.New("fleet: TLS config required")
	}
	conn, err := grpc.NewClient(c.addr, grpc.WithTransportCredentials(credentials.NewTLS(c.tls)))
	if err != nil {
		return false, err
	}
	defer conn.Close()

	// Attach auth metadata: prefer per-agent token; fall back to enroll token.
	// If neither is set and WithAuth was not called, proceed without metadata
	// (backward-compatible with tests that inject metadata externally).
	if c.token != "" {
		ctx = metadata.AppendToOutgoingContext(ctx, "marshal-token", c.token)
	} else if c.enrollTok != "" {
		ctx = metadata.AppendToOutgoingContext(ctx, "marshal-enroll", c.enrollTok)
	} else if c.persistTok != nil {
		return false, errors.New("fleet: no token or enrollment token")
	}

	stream, err := pb.NewFleetClient(conn).Connect(ctx)
	if err != nil {
		return false, err
	}

	var sendMu sync.Mutex
	send := func(m *pb.AgentMessage) error {
		sendMu.Lock()
		defer sendMu.Unlock()
		return stream.Send(m)
	}

	if err := send(&pb.AgentMessage{Msg: &pb.AgentMessage_Hello{
		Hello: &pb.Hello{AgentName: c.name, MarshalVersion: c.version},
	}}); err != nil {
		return false, err
	}
	// Receive the HelloAck to seed the metric and log watermarks.
	var watermark, logWM int64
	if ack, err := stream.Recv(); err != nil {
		return true, err
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

	// Receiver goroutine: handle commands until the stream errors.
	recvErr := make(chan error, 1)
	go func() {
		for {
			msg, err := stream.Recv()
			if err != nil {
				recvErr <- err
				return
			}
			if cmd := msg.GetCommand(); cmd != nil && c.commands != nil {
				res := c.commands(cmd)
				_ = send(&pb.AgentMessage{Msg: &pb.AgentMessage_Result{
					Result: &pb.CommandResult{RequestId: cmd.GetRequestId(), Result: res},
				}})
			}
		}
	}()

	if err := c.pushSnapshot(send); err != nil {
		return true, err
	}
	if err := c.pushMetrics(send, &watermark); err != nil {
		return true, err
	}
	if err := c.pushLogs(send, &logWM); err != nil {
		return true, err
	}

	t := time.NewTicker(c.interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return true, ctx.Err()
		case err := <-recvErr:
			return true, err
		case <-t.C:
			if err := c.pushSnapshot(send); err != nil {
				return true, err
			}
			if err := c.pushMetrics(send, &watermark); err != nil {
				return true, err
			}
			if err := c.pushLogs(send, &logWM); err != nil {
				return true, err
			}
		}
	}
}

func (c *Client) pushSnapshot(send func(*pb.AgentMessage) error) error {
	return send(&pb.AgentMessage{Msg: &pb.AgentMessage_Snapshot{
		Snapshot: &pb.StateSnapshot{Procs: c.snapshot()},
	}})
}

// pushMetrics ships local rows newer than *watermark; on success advances it to
// the max ts shipped. No-op when metrics shipping is disabled or nothing is new.
func (c *Client) pushMetrics(send func(*pb.AgentMessage) error, watermark *int64) error {
	if c.metrics == nil {
		return nil
	}
	samples := c.metrics(*watermark)
	if len(samples) == 0 {
		return nil
	}
	if err := send(&pb.AgentMessage{Msg: &pb.AgentMessage_Metrics{
		Metrics: &pb.MetricBatch{Samples: samples},
	}}); err != nil {
		return err
	}
	var mx int64
	for _, s := range samples {
		if s.GetTsMs() > mx {
			mx = s.GetTsMs()
		}
	}
	if mx > *watermark {
		*watermark = mx
	}
	return nil
}

// pushLogs ships local lines newer than *watermark; on success advances it to
// the max ts shipped. No-op when log shipping is disabled or nothing is new.
func (c *Client) pushLogs(send func(*pb.AgentMessage) error, watermark *int64) error {
	if c.logs == nil {
		return nil
	}
	lines := c.logs(*watermark)
	if len(lines) == 0 {
		return nil
	}
	if err := send(&pb.AgentMessage{Msg: &pb.AgentMessage_Logs{
		Logs: &pb.LogBatch{Lines: lines},
	}}); err != nil {
		return err
	}
	var mx int64
	for _, l := range lines {
		if l.GetTsMs() > mx {
			mx = l.GetTsMs()
		}
	}
	if mx > *watermark {
		*watermark = mx
	}
	return nil
}
