package audit

import (
	"bufio"
	"encoding/json"
	"os"
)

// ReadOptions filters the result of Read.
type ReadOptions struct {
	Limit        int  // 0 = all; otherwise the most recent N (after filtering)
	FailuresOnly bool // exclude OutcomeSuccess
}

// Read returns events from path and its rotated companion (path+".1") in
// chronological order, oldest first. The rotated file is read before the current
// one. Corrupt or blank lines are skipped. A missing file is not an error.
func Read(path string, opts ReadOptions) ([]Event, error) {
	var out []Event
	for _, p := range []string{path + ".1", path} {
		evs, err := readFile(p)
		if err != nil {
			return nil, err
		}
		out = append(out, evs...)
	}
	if opts.FailuresOnly {
		var kept []Event
		for _, e := range out {
			if e.Outcome != OutcomeSuccess {
				kept = append(kept, e)
			}
		}
		out = kept
	}
	if opts.Limit > 0 && len(out) > opts.Limit {
		out = out[len(out)-opts.Limit:]
	}
	return out, nil
}

// readFile parses one JSONL file, skipping corrupt/blank lines. A missing file
// yields a nil slice and no error.
func readFile(p string) ([]Event, error) {
	f, err := os.Open(p)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	defer f.Close()
	var out []Event
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 1<<20)
	for sc.Scan() {
		line := sc.Bytes()
		if len(line) == 0 {
			continue
		}
		var e Event
		if err := json.Unmarshal(line, &e); err != nil {
			continue // tolerate a corrupt/partial line
		}
		out = append(out, e)
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	return out, nil
}
