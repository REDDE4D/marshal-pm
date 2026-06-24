package errsig

import "sort"

// Line is one stderr line tagged with its originating agent.
type Line struct {
	TsMs  int64
	Label string // "app#instance"
	Text  string
	Agent string
}

// Sig is one error signature's rollup over the window.
type Sig struct {
	Id        string
	Sample    string // first raw occurrence, for display
	Source    string // best-effort file:line, "" if unknown
	Agent     string // representative (most recent) origin
	Proc      string // representative (most recent) proc label
	Affected  []string
	Count     int
	FirstUnix int64
	LastUnix  int64
	Buckets   []int
}

// Cluster holds the headline totals for the window.
type Cluster struct {
	Errors        int
	Signatures    int
	AffectedProcs int
	LastErrorUnix int64
}

// Result is the full /api/errors payload (pre-JSON).
type Result struct {
	Cluster    Cluster
	Signatures []Sig
}

// Aggregate folds error lines (ascending by Label,TsMs) into the cluster totals
// and the signature ledger. Lines before sinceMs or failing IsError are ignored.
func Aggregate(lines []Line, sinceMs, nowMs int64, nBuckets int) Result {
	if nBuckets < 1 {
		nBuckets = 1
	}
	span := nowMs - sinceMs
	if span <= 0 {
		span = 1
	}
	type acc struct {
		sig      *Sig
		affected map[string]bool
	}
	m := map[string]*acc{}
	var order []*acc
	cluster := Cluster{}
	allProcs := map[string]bool{}

	for i := range lines {
		ln := lines[i]
		if ln.TsMs < sinceMs || !IsError(ln.Text) {
			continue
		}
		cluster.Errors++
		sec := ln.TsMs / 1000
		if sec > cluster.LastErrorUnix {
			cluster.LastErrorUnix = sec
		}
		allProcs[ln.Label] = true
		id := Signature(ln.Text)
		a := m[id]
		if a == nil {
			win := []string{ln.Text}
			if isTraceHeader(ln.Text) {
				for j := i + 1; j < len(lines) && len(win) < 6; j++ {
					if lines[j].Label != ln.Label || lines[j].Agent != ln.Agent {
						break
					}
					win = append(win, lines[j].Text)
				}
			}
			a = &acc{
				sig: &Sig{
					Id: id, Sample: ln.Text, Source: Source(win),
					Buckets: make([]int, nBuckets), FirstUnix: sec,
				},
				affected: map[string]bool{},
			}
			m[id] = a
			order = append(order, a)
		}
		s := a.sig
		s.Count++
		s.LastUnix = sec
		s.Agent = ln.Agent
		s.Proc = ln.Label
		a.affected[ln.Label] = true
		b := int((ln.TsMs - sinceMs) * int64(nBuckets) / span)
		if b < 0 {
			b = 0
		}
		if b >= nBuckets {
			b = nBuckets - 1
		}
		s.Buckets[b]++
	}

	sigs := make([]Sig, 0, len(order))
	for _, a := range order {
		aff := make([]string, 0, len(a.affected))
		for p := range a.affected {
			aff = append(aff, p)
		}
		sort.Strings(aff)
		a.sig.Affected = aff
		sigs = append(sigs, *a.sig)
	}
	sort.SliceStable(sigs, func(i, j int) bool {
		if sigs[i].Count != sigs[j].Count {
			return sigs[i].Count > sigs[j].Count
		}
		return sigs[i].LastUnix > sigs[j].LastUnix
	})
	cluster.Signatures = len(sigs)
	cluster.AffectedProcs = len(allProcs)
	return Result{Cluster: cluster, Signatures: sigs}
}
