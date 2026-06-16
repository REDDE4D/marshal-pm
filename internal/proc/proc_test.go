package proc

import (
	"os"
	"syscall"
	"testing"
	"time"
)

func TestStartAndWaitSuccess(t *testing.T) {
	p, err := Start(Spec{Cmd: "sh", Args: []string{"-c", "exit 0"}})
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	if p.Pid() <= 0 {
		t.Fatalf("pid = %d, want > 0", p.Pid())
	}
	if err := p.Wait(); err != nil {
		t.Fatalf("wait returned error for exit 0: %v", err)
	}
}

func TestWaitReportsFailure(t *testing.T) {
	p, err := Start(Spec{Cmd: "sh", Args: []string{"-c", "exit 3"}})
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	if err := p.Wait(); err == nil {
		t.Fatal("wait returned nil for exit 3, want error")
	}
}

func TestInstanceIDInjected(t *testing.T) {
	// Child writes its instance id to a temp file we then read.
	f, err := os.CreateTemp(t.TempDir(), "iid")
	if err != nil {
		t.Fatal(err)
	}
	p, err := Start(Spec{
		Cmd:        "sh",
		Args:       []string{"-c", "printf %s \"$MARSHAL_INSTANCE_ID\" > " + f.Name()},
		InstanceID: 2,
	})
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	if err := p.Wait(); err != nil {
		t.Fatalf("wait: %v", err)
	}
	data, err := os.ReadFile(f.Name())
	if err != nil {
		t.Fatalf("read temp file: %v", err)
	}
	if string(data) != "2" {
		t.Fatalf("MARSHAL_INSTANCE_ID = %q, want 2", string(data))
	}
}

func TestSignalStopsProcess(t *testing.T) {
	p, err := Start(Spec{Cmd: "sh", Args: []string{"-c", "sleep 30"}})
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	done := make(chan struct{})
	go func() { _ = p.Wait(); close(done) }()
	time.Sleep(100 * time.Millisecond)
	if err := p.Signal(syscall.SIGTERM); err != nil {
		t.Fatalf("signal: %v", err)
	}
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("process did not exit after SIGTERM")
	}
}
