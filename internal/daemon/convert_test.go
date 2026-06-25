package daemon

import (
	"testing"

	"github.com/REDDE4D/marshal-pm/internal/pb"
)

func TestAppSpecToConfigCopiesGitSource(t *testing.T) {
	spec := &pb.AppSpec{
		Name: "web", Cmd: "./server", Instances: 1,
		Source: &pb.GitSource{Repo: "https://example/r.git", Ref: "main", Build: "go build -o server .", Subdir: "cmd"},
	}
	app, err := appSpecToConfig(spec)
	if err != nil {
		t.Fatalf("appSpecToConfig: %v", err)
	}
	if app.Source == nil {
		t.Fatal("Source not copied")
	}
	if app.Source.Repo != "https://example/r.git" || app.Source.Ref != "main" ||
		app.Source.Build != "go build -o server ." || app.Source.Subdir != "cmd" {
		t.Fatalf("Source mismatch: %+v", app.Source)
	}
}

func TestAppSpecToConfigReadsLogs(t *testing.T) {
	sz := int32(50)
	age := int32(0)
	comp := false
	app, err := appSpecToConfig(&pb.AppSpec{
		Name: "api", Cmd: "./api",
		Logs: &pb.LogRetention{MaxSizeMb: &sz, MaxAgeDays: &age, Compress: &comp},
	})
	if err != nil {
		t.Fatal(err)
	}
	if app.Logs == nil || app.Logs.MaxSizeMB == nil || *app.Logs.MaxSizeMB != 50 {
		t.Fatalf("max_size_mb not copied: %+v", app.Logs)
	}
	if app.Logs.MaxAgeDays == nil || *app.Logs.MaxAgeDays != 0 {
		t.Fatalf("explicit age 0 must be preserved")
	}
	if app.Logs.MaxBackups != nil {
		t.Fatalf("omitted max_backups must stay nil")
	}
}
