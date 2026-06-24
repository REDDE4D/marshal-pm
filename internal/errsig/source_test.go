package errsig

import "testing"

func TestSourceGoPanic(t *testing.T) {
	win := []string{
		"panic: runtime error: invalid memory address",
		"goroutine 1 [running]:",
		"main.work(...)",
		"\t/home/app/worker.go:142 +0x1a",
	}
	if got := Source(win); got != "worker.go:142" {
		t.Errorf("Source = %q, want worker.go:142", got)
	}
}

func TestSourcePythonTraceback(t *testing.T) {
	win := []string{
		"Traceback (most recent call last):",
		"  File \"/srv/app/main.py\", line 88, in handler",
		"ValueError: bad input",
	}
	if got := Source(win); got != "main.py:88" {
		t.Errorf("Source = %q, want main.py:88", got)
	}
}

func TestSourceNone(t *testing.T) {
	if got := Source([]string{"connection refused"}); got != "" {
		t.Errorf("Source = %q, want empty", got)
	}
}
