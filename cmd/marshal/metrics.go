package main

import (
	"context"
	"fmt"
	"io"
	"time"

	"github.com/spf13/cobra"

	"marshal/internal/pb"
)

func metricsCmd() *cobra.Command {
	var since, bucket time.Duration
	var cpuOnly, memOnly bool
	cmd := &cobra.Command{
		Use:   "metrics <name|id>",
		Short: "Show CPU/memory history for an app/instance",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return withClient(func(ctx context.Context, c pb.DaemonClient) error {
				resp, err := c.MetricsHistory(ctx, &pb.MetricsHistoryRequest{
					Selector: args[0],
					SinceMs:  since.Milliseconds(),
					BucketMs: bucket.Milliseconds(),
				})
				if err != nil {
					return err
				}
				printMetrics(cmd.OutOrStdout(), resp, args[0], since, cpuOnly, memOnly)
				return nil
			})
		},
	}
	cmd.Flags().DurationVar(&since, "since", time.Hour, "history window (e.g. 30m, 6h)")
	cmd.Flags().DurationVar(&bucket, "bucket", 0, "bucket width (0 = auto)")
	cmd.Flags().BoolVar(&cpuOnly, "cpu", false, "show only CPU")
	cmd.Flags().BoolVar(&memOnly, "mem", false, "show only memory")
	return cmd
}

// printMetrics renders the history as labeled sparklines with min/avg/max.
func printMetrics(w io.Writer, resp *pb.MetricsHistoryResponse, selector string, since time.Duration, cpuOnly, memOnly bool) {
	buckets := resp.GetBuckets()
	if len(buckets) == 0 {
		fmt.Fprintf(w, "no metric history for %q in the last %s\n", selector, since)
		return
	}
	cpu := make([]float64, len(buckets))
	mem := make([]float64, len(buckets))
	for i, b := range buckets {
		cpu[i] = b.GetCpuAvg()
		mem[i] = float64(b.GetMemAvg())
	}
	fmt.Fprintf(w, "%s — last %s, %d buckets\n", selector, since, len(buckets))
	if !memOnly {
		mn, av, mx := summarize(cpu)
		fmt.Fprintf(w, "CPU  %s  min %.1f%%  avg %.1f%%  max %.1f%%\n", sparkline(cpu), mn, av, mx)
	}
	if !cpuOnly {
		mn, av, mx := summarize(mem)
		fmt.Fprintf(w, "MEM  %s  min %s  avg %s  max %s\n",
			sparkline(mem), humanizeBytes(int64(mn)), humanizeBytes(int64(av)), humanizeBytes(int64(mx)))
	}
}
