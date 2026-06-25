package startup

import (
	"strings"
	"testing"
)

func serverConfig() Config {
	return Config{
		Binary: "/usr/bin/marshal", User: "tg", Home: "/home/tg", UID: 501,
		Args:  []string{"server", "--http-listen", ":9001"},
		Label: "marshal-server",
	}
}

func TestSystemdServerUnitUsesLabelAndArgs(t *testing.T) {
	p := systemd{}.InstallPlan(serverConfig())
	if p.Label != "marshal-server.service" {
		t.Errorf("label = %q, want marshal-server.service", p.Label)
	}
	if !strings.Contains(p.Content, "ExecStart=/usr/bin/marshal server --http-listen :9001") {
		t.Errorf("ExecStart wrong:\n%s", p.Content)
	}
}

func TestLaunchdServerLabelAndArgs(t *testing.T) {
	p := launchd{}.InstallPlan(serverConfig())
	if p.Label != "com.marshal-server" {
		t.Errorf("label = %q, want com.marshal-server", p.Label)
	}
	if !strings.Contains(p.Content, "<string>server</string>") || !strings.Contains(p.Content, "<string>--http-listen</string>") {
		t.Errorf("ProgramArguments missing server args:\n%s", p.Content)
	}
}

func TestDefaultsPreserveDaemonService(t *testing.T) {
	// No Label/Args → the original daemon service, unchanged.
	c := Config{Binary: "/usr/bin/marshal", Home: "/home/tg"}
	sp := systemd{}.InstallPlan(c)
	if sp.Label != "marshal.service" || !strings.Contains(sp.Content, "ExecStart=/usr/bin/marshal daemon") {
		t.Errorf("systemd default changed: label=%q\n%s", sp.Label, sp.Content)
	}
	lp := launchd{}.InstallPlan(c)
	if lp.Label != "com.marshal.daemon" {
		t.Errorf("launchd default label = %q, want com.marshal.daemon", lp.Label)
	}
}
