package startup

import "testing"

func TestDetectDarwin(t *testing.T) {
	p, err := detect("darwin", func(string) bool { return false })
	if err != nil {
		t.Fatalf("detect: %v", err)
	}
	if _, ok := p.(launchd); !ok {
		t.Fatalf("want launchd, got %T", p)
	}
}

func TestDetectLinuxWithSystemd(t *testing.T) {
	p, err := detect("linux", func(string) bool { return true })
	if err != nil {
		t.Fatalf("detect: %v", err)
	}
	if _, ok := p.(systemd); !ok {
		t.Fatalf("want systemd, got %T", p)
	}
}

func TestDetectLinuxNoSystemd(t *testing.T) {
	if _, err := detect("linux", func(string) bool { return false }); err == nil {
		t.Fatal("want error when systemd absent")
	}
}

func TestDetectUnsupported(t *testing.T) {
	if _, err := detect("plan9", func(string) bool { return true }); err == nil {
		t.Fatal("want error for unsupported GOOS")
	}
}
