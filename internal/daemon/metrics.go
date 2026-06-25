package daemon

import (
	"context"
	"time"

	"github.com/REDDE4D/marshal-pm/internal/metricstore"
	"github.com/REDDE4D/marshal-pm/internal/pb"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

const defaultHistory = time.Hour

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
	bucketMs := metricstore.AutoBucketMs(sinceMs, req.GetBucketMs())
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
	for _, b := range metricstore.MergeBuckets(series) {
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
