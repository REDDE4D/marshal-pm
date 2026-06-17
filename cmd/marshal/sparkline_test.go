package main

import "testing"

func TestSparklineEmpty(t *testing.T) {
	if got := sparkline(nil); got != "" {
		t.Fatalf("sparkline(nil) = %q, want empty", got)
	}
}

func TestSparklineLengthMatchesInput(t *testing.T) {
	got := []rune(sparkline([]float64{1, 2, 3, 4, 5}))
	if len(got) != 5 {
		t.Fatalf("len = %d, want 5", len(got))
	}
}

func TestSparklineRampLowToHigh(t *testing.T) {
	got := []rune(sparkline([]float64{0, 50, 100}))
	if got[0] != '▁' {
		t.Fatalf("first rune = %q, want ▁", string(got[0]))
	}
	if got[len(got)-1] != '█' {
		t.Fatalf("last rune = %q, want █", string(got[len(got)-1]))
	}
}

func TestSparklineAllEqual(t *testing.T) {
	got := []rune(sparkline([]float64{7, 7, 7}))
	for _, r := range got {
		if r != got[0] {
			t.Fatalf("all-equal input should render one rune, got %q", string(got))
		}
	}
}

func TestSummarize(t *testing.T) {
	min, avg, max := summarize([]float64{2, 4, 6})
	if min != 2 || max != 6 || avg != 4 {
		t.Fatalf("summarize = (%v,%v,%v), want (2,4,6)", min, avg, max)
	}
}
