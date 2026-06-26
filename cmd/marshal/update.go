package main

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/spf13/cobra"

	"github.com/REDDE4D/marshal-pm/internal/client"
	"github.com/REDDE4D/marshal-pm/internal/pb"
	"github.com/REDDE4D/marshal-pm/internal/store"
	"github.com/REDDE4D/marshal-pm/internal/updatecheck"
)

// updateBanner returns the one-line "update available" hint, or "" when the
// daemon reports up-to-date / has no data yet / info is nil.
func updateBanner(info *pb.UpdateInfo) string {
	if info == nil || !info.GetOutdated() || info.GetLatest() == "" {
		return ""
	}
	return fmt.Sprintf("marshal: update available — %s (current %s) → %s",
		info.GetLatest(), info.GetCurrent(), updatecheck.DefaultReleasesURL)
}

// maybePrintUpdateBanner prints the update hint to stderr after a command, but
// only when: the opt-out env var is unset, stderr is a terminal, and a daemon is
// already running (it never spawns one). Any error is swallowed — the hint is
// strictly best-effort and must never affect the command's outcome.
func maybePrintUpdateBanner(cmd *cobra.Command) {
	if os.Getenv("MARSHAL_NO_UPDATE_CHECK") != "" {
		return
	}
	if !isTerminal(cmd.ErrOrStderr()) {
		return
	}
	st, err := store.New()
	if err != nil {
		return
	}
	c, conn, err := client.ConnectExisting(st)
	if err != nil {
		return
	}
	defer conn.Close()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	info, err := c.UpdateStatus(ctx, &pb.Empty{})
	if err != nil {
		return
	}
	if b := updateBanner(info); b != "" {
		fmt.Fprintln(cmd.ErrOrStderr(), b)
	}
}
