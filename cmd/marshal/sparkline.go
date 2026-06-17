package main

import "strings"

// sparkline renders values as Unicode block characters scaled to the data's own
// min..max range. Empty input yields "". A flat series renders the lowest block.
func sparkline(vals []float64) string {
	if len(vals) == 0 {
		return ""
	}
	blocks := []rune("▁▂▃▄▅▆▇█")
	min, max := vals[0], vals[0]
	for _, v := range vals {
		if v < min {
			min = v
		}
		if v > max {
			max = v
		}
	}
	span := max - min
	var b strings.Builder
	for _, v := range vals {
		idx := 0
		if span > 0 {
			idx = int((v-min)/span*float64(len(blocks)-1) + 0.5)
		}
		b.WriteRune(blocks[idx])
	}
	return b.String()
}

// summarize returns the min, mean, and max of vals (all zero for empty input).
func summarize(vals []float64) (min, avg, max float64) {
	if len(vals) == 0 {
		return 0, 0, 0
	}
	min, max = vals[0], vals[0]
	var sum float64
	for _, v := range vals {
		if v < min {
			min = v
		}
		if v > max {
			max = v
		}
		sum += v
	}
	return min, sum / float64(len(vals)), max
}
