package daemon

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/REDDE4D/marshal-pm/internal/logs"
	"github.com/REDDE4D/marshal-pm/internal/manager"
	"github.com/REDDE4D/marshal-pm/internal/pb"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// fakeLogStream is an in-memory pb.Daemon_LogsServer.
type fakeLogStream struct {
	pb.Daemon_LogsServer
	ctx  context.Context
	recv chan *pb.LogLine
}

func newFakeLogStream(ctx context.Context) *fakeLogStream {
	return &fakeLogStream{ctx: ctx, recv: make(chan *pb.LogLine, 256)}
}
func (f *fakeLogStream) Send(l *pb.LogLine) error { f.recv <- l; return nil }
func (f *fakeLogStream) Context() context.Context { return f.ctx }

func TestLogsBackfillNoFollow(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	reg := logs.NewRegistry(t.TempDir())
	mgr := manager.New(ctx, manager.WithLogs(reg))
	srv := &Server{mgr: mgr, logs: reg}
	defer mgr.StopAll()

	if _, err := srv.Start(ctx, &pb.StartRequest{Apps: []*pb.AppSpec{
		{Name: "a", Cmd: "sh", Args: []string{"-c", "echo one; echo two; sleep 30"}, Instances: 1},
	}}); err != nil {
		t.Fatalf("Start: %v", err)
	}

	// Wait until the ring buffer has both lines.
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if len(reg.For("a#0").Backfill(10)) >= 2 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}

	stream := newFakeLogStream(ctx)
	if err := srv.Logs(&pb.LogRequest{Target: "a", Lines: 10, Follow: false}, stream); err != nil {
		t.Fatalf("Logs: %v", err)
	}
	close(stream.recv)
	var texts []string
	for l := range stream.recv {
		texts = append(texts, l.GetLine())
	}
	if len(texts) < 2 || texts[0] != "one" || texts[1] != "two" {
		t.Fatalf("got %v, want [one two ...]", texts)
	}
}

func TestLogsUnknownTargetNotFound(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	reg := logs.NewRegistry(t.TempDir())
	srv := &Server{mgr: manager.New(ctx, manager.WithLogs(reg)), logs: reg}
	err := srv.Logs(&pb.LogRequest{Target: "ghost"}, newFakeLogStream(ctx))
	if status.Code(err) != codes.NotFound {
		t.Fatalf("got %v, want NotFound", err)
	}
}

func TestStreamMatch(t *testing.T) {
	cases := []struct {
		f      pb.LogStream
		stderr bool
		want   bool
	}{
		{pb.LogStream_LOG_STREAM_UNSPECIFIED, false, true},
		{pb.LogStream_LOG_STREAM_UNSPECIFIED, true, true},
		{pb.LogStream_LOG_STREAM_STDOUT, false, true},
		{pb.LogStream_LOG_STREAM_STDOUT, true, false},
		{pb.LogStream_LOG_STREAM_STDERR, true, true},
		{pb.LogStream_LOG_STREAM_STDERR, false, false},
	}
	for _, c := range cases {
		if got := streamMatch(c.f, c.stderr); got != c.want {
			t.Fatalf("streamMatch(%v,%v)=%v want %v", c.f, c.stderr, got, c.want)
		}
	}
}

func TestBackfillRoutingPerStreamReadsFiles(t *testing.T) {
	reg := logs.NewRegistry(t.TempDir())
	out, errw := reg.WriterPair("app#0")
	_, _ = out.Write([]byte("o1\no2\n"))
	_, _ = errw.Write([]byte("e1\n"))
	labeled := reg.ResolveLabeled([]string{"app#0"})

	got := backfillLines(labeled, 10, pb.LogStream_LOG_STREAM_STDERR)
	if len(got) != 1 || got[0].line.Text != "e1" || !got[0].line.Stderr {
		t.Fatalf("stderr-only backfill wrong: %+v", got)
	}
}

func TestMergedBackfillReadsFilesWhenRingCold(t *testing.T) {
	dir := t.TempDir()
	// Pre-existing on-disk history, as if written before a daemon restart.
	if err := os.WriteFile(filepath.Join(dir, "app#0.out.log"), []byte("o1\no2\no3\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "app#0.err.log"), []byte("e1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	reg := logs.NewRegistry(dir)
	reg.WriterPair("app#0") // create the sink with a cold (empty) ring; files untouched
	labeled := reg.ResolveLabeled([]string{"app#0"})

	got := backfillLines(labeled, 10, pb.LogStream_LOG_STREAM_UNSPECIFIED)
	if len(got) != 4 {
		t.Fatalf("cold-ring merged backfill must read the 4 on-disk lines, got %d", len(got))
	}
}

func TestLogsFollowStreamsLiveLines(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	reg := logs.NewRegistry(t.TempDir())
	mgr := manager.New(ctx, manager.WithLogs(reg))
	srv := &Server{mgr: mgr, logs: reg}
	defer mgr.StopAll()

	if _, err := srv.Start(ctx, &pb.StartRequest{Apps: []*pb.AppSpec{
		{Name: "a", Cmd: "sh", Args: []string{"-c", "i=0; while true; do echo tick-$i; i=$((i+1)); sleep 0.1; done"}, Instances: 1},
	}}); err != nil {
		t.Fatalf("Start: %v", err)
	}

	streamCtx, streamCancel := context.WithCancel(ctx)
	stream := newFakeLogStream(streamCtx)
	done := make(chan error, 1)
	go func() { done <- srv.Logs(&pb.LogRequest{Target: "a", Lines: 0, Follow: true}, stream) }()

	select {
	case l := <-stream.recv:
		if l.GetLine() == "" {
			t.Fatalf("empty line")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("no live line within 3s")
	}
	streamCancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Logs did not return after stream cancel")
	}
}
