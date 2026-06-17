// Package fleet holds the agent-side client that streams local process state to
// the Marshal central server. It is additive: with no server configured it never
// runs, and a server outage never affects local supervision.
package fleet

import (
	"context"
	"log"
	"time"

	"marshal/internal/pb"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

// SnapshotFunc returns the agent's current process state.
type SnapshotFunc func() []*pb.ProcInfo

// Client maintains one outbound stream to the central server.
type Client struct {
	addr     string
	name     string
	version  string
	snapshot SnapshotFunc
	interval time.Duration
	minBO    time.Duration
	maxBO    time.Duration
}

// Option configures a Client.
type Option func(*Client)

// WithInterval sets the snapshot push cadence (also the liveness heartbeat).
func WithInterval(d time.Duration) Option { return func(c *Client) { c.interval = d } }

// WithBackoff sets the reconnect backoff bounds.
func WithBackoff(min, max time.Duration) Option {
	return func(c *Client) { c.minBO, c.maxBO = min, max }
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
		if err != nil {
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

// connectOnce dials, sends Hello, then pushes snapshots until the stream errors
// or ctx is canceled. The bool reports whether the stream was established.
func (c *Client) connectOnce(ctx context.Context) (bool, error) {
	conn, err := grpc.NewClient(c.addr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		return false, err
	}
	defer conn.Close()

	stream, err := pb.NewFleetClient(conn).Connect(ctx)
	if err != nil {
		return false, err
	}
	if err := stream.Send(&pb.AgentMessage{Msg: &pb.AgentMessage_Hello{
		Hello: &pb.Hello{AgentName: c.name, MarshalVersion: c.version},
	}}); err != nil {
		return false, err
	}
	if err := c.push(stream); err != nil { // immediate first snapshot
		return true, err
	}

	t := time.NewTicker(c.interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return true, ctx.Err()
		case <-t.C:
			if err := c.push(stream); err != nil {
				return true, err
			}
		}
	}
}

func (c *Client) push(stream pb.Fleet_ConnectClient) error {
	return stream.Send(&pb.AgentMessage{Msg: &pb.AgentMessage_Snapshot{
		Snapshot: &pb.StateSnapshot{Procs: c.snapshot()},
	}})
}
