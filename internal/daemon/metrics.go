package daemon

import (
	"context"
	"sort"
	"time"

	"marshal/internal/metricstore"
	"marshal/internal/pb"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

const (
	targetBuckets  = 60
	defaultHistory = time.Hour
	minBucketMs    = 1000
)

// MetricsHistory returns time-bucketed CPU/RSS history for the selected app,
// summed across its instances per bucket.
func (s *Server) MetricsHistory(_ context.Context, req *pb.MetricsHistoryRequest) (*pb.MetricsHistoryResponse, error) {
	if s.mdb == nil {
		return nil, status.Error(codes.Unavailable, "metrics history not configured")
	}
	snaps, err := s.mgr.Describe(req.GetSelector())
	if err != nil {
		return nil, status.Errorf(codes.NotFound, "%v", err)
	}

	sinceMs := req.GetSinceMs()
	if sinceMs <= 0 {
		sinceMs = int64(defaultHistory / time.Millisecond)
	}
	bucketMs := req.GetBucketMs()
	if bucketMs <= 0 {
		bucketMs = sinceMs / targetBuckets
		if bucketMs < minBucketMs {
			bucketMs = minBucketMs
		}
	}
	lowerMs := time.Now().UnixMilli() - sinceMs

	var series [][]metricstore.Bucket
	for _, sn := range snaps {
		bs, err := s.mdb.Query(metricstore.QueryReq{Label: sn.Label, SinceMs: lowerMs, BucketMs: bucketMs})
		if err != nil {
			return nil, status.Errorf(codes.Internal, "query metrics: %v", err)
		}
		series = append(series, bs)
	}

	resp := &pb.MetricsHistoryResponse{}
	for _, b := range mergeBuckets(series) {
		resp.Buckets = append(resp.Buckets, &pb.MetricBucket{
			TsMs:   b.TsMs,
			CpuAvg: b.CpuAvg,
			CpuMax: b.CpuMax,
			MemAvg: b.MemAvg,
			MemMax: b.MemMax,
		})
	}
	return resp, nil
}

// mergeBuckets combines per-instance series sharing a bucket timestamp: averages
// are summed (whole-app total), maxes take the max across instances. Result is
// ordered oldest first.
func mergeBuckets(series [][]metricstore.Bucket) []metricstore.Bucket {
	byTs := map[int64]*metricstore.Bucket{}
	var order []int64
	for _, bs := range series {
		for _, b := range bs {
			cur, ok := byTs[b.TsMs]
			if !ok {
				nb := b
				byTs[b.TsMs] = &nb
				order = append(order, b.TsMs)
				continue
			}
			cur.CpuAvg += b.CpuAvg
			cur.MemAvg += b.MemAvg
			if b.CpuMax > cur.CpuMax {
				cur.CpuMax = b.CpuMax
			}
			if b.MemMax > cur.MemMax {
				cur.MemMax = b.MemMax
			}
		}
	}
	sort.Slice(order, func(i, j int) bool { return order[i] < order[j] })
	out := make([]metricstore.Bucket, 0, len(order))
	for _, ts := range order {
		out = append(out, *byTs[ts])
	}
	return out
}
