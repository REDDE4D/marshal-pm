package logs

import (
	"bufio"
	"compress/gzip"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// fileBackfill returns up to n lines from one stream's on-disk segments,
// scanning newest segment first and stopping once n lines are gathered.
// Returned lines are ordered oldest->newest, carry Stderr set and a zero Ts,
// and absent files are not an error.
func fileBackfill(dir, label string, stderr bool, n int) ([]Line, error) {
	stream := "out"
	if stderr {
		stream = "err"
	}
	base := label + "." + stream // e.g. "app#0.out"
	active := base + ".log"

	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var rotated []string
	for _, e := range entries {
		name := e.Name()
		if strings.HasPrefix(name, base+"-") && (strings.HasSuffix(name, ".log") || strings.HasSuffix(name, ".log.gz")) {
			rotated = append(rotated, name)
		}
	}
	sort.Sort(sort.Reverse(sort.StringSlice(rotated))) // newest first
	order := append([]string{active}, rotated...)      // active is newest

	var segs [][]string // newest segment first
	total := 0
	for _, fn := range order {
		if n > 0 && total >= n {
			break
		}
		lines, err := readSegmentLines(filepath.Join(dir, fn))
		if err != nil {
			return nil, err
		}
		if len(lines) == 0 {
			continue
		}
		segs = append(segs, lines)
		total += len(lines)
	}

	var out []Line
	for i := len(segs) - 1; i >= 0; i-- { // oldest segment first
		for _, t := range segs[i] {
			out = append(out, Line{Stderr: stderr, Text: t})
		}
	}
	if n > 0 && len(out) > n {
		out = out[len(out)-n:]
	}
	return out, nil
}

// readSegmentLines reads all newline-terminated lines from a log segment,
// gunzipping when the path ends in .gz. A missing file yields no lines.
func readSegmentLines(path string) ([]string, error) {
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil // concurrent rotation removed it; skip
		}
		return nil, err
	}
	defer f.Close()

	var r io.Reader = f
	if strings.HasSuffix(path, ".gz") {
		zr, err := gzip.NewReader(f)
		if err != nil {
			return nil, nil // truncated/partial gz; skip rather than fail the whole read
		}
		defer zr.Close()
		r = zr
	}

	var lines []string
	br := bufio.NewReader(r)
	for {
		line, err := br.ReadString('\n')
		if len(line) > 0 {
			lines = append(lines, strings.TrimSuffix(line, "\n"))
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
	}
	// A trailing newline yields a final empty element; drop it.
	if k := len(lines); k > 0 && lines[k-1] == "" {
		lines = lines[:k-1]
	}
	return lines, nil
}
