package main

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"marshal/internal/pb"
)

func TestPrintMetricsRendersRows(t *testing.T) {
	resp := &pb.MetricsHistoryResponse{Buckets: []*pb.MetricBucket{
		{TsMs: 1000, CpuAvg: 10, CpuMax: 12, MemAvg: 100, MemMax: 120},
		{TsMs: 2000, CpuAvg: 20, CpuMax: 25, MemAvg: 200, MemMax: 240},
	}}
	var buf bytes.Buffer
	printMetrics(&buf, resp, "web", time.Hour, false, false)
	out := buf.String()
	if !strings.Contains(out, "CPU") || !strings.Contains(out, "MEM") {
		t.Fatalf("output missing CPU/MEM rows:\n%s", out)
	}
}

func TestPrintMetricsEmpty(t *testing.T) {
	var buf bytes.Buffer
	printMetrics(&buf, &pb.MetricsHistoryResponse{}, "web", time.Hour, false, false)
	if !strings.Contains(buf.String(), "no metric history") {
		t.Fatalf("empty output = %q, want a 'no metric history' notice", buf.String())
	}
}

func TestPrintMetricsCPUOnly(t *testing.T) {
	resp := &pb.MetricsHistoryResponse{Buckets: []*pb.MetricBucket{
		{TsMs: 1000, CpuAvg: 10, CpuMax: 12, MemAvg: 100, MemMax: 120},
	}}
	var buf bytes.Buffer
	printMetrics(&buf, resp, "web", time.Hour, true, false)
	if strings.Contains(buf.String(), "MEM") {
		t.Fatalf("--cpu output should not contain MEM:\n%s", buf.String())
	}
}
