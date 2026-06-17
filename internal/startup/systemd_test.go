package startup

import (
	"strings"
	"testing"
)

func linuxConfig(system bool) Config {
	return Config{
		Binary:   "/usr/local/bin/marshal",
		User:     "alice",
		Home:     "/home/alice",
		System:   system,
		StageDir: "/home/alice/.marshal",
		UID:      1000,
	}
}

func TestSystemdUserUnit(t *testing.T) {
	p := systemd{}.InstallPlan(linuxConfig(false))
	if p.NeedsRoot {
		t.Fatal("user plan must not need root")
	}
	if p.UnitPath != "/home/alice/.config/systemd/user/marshal.service" {
		t.Fatalf("UnitPath = %s", p.UnitPath)
	}
	for _, w := range []string{
		"ExecStart=/usr/local/bin/marshal daemon",
		"Restart=on-failure",
		"Environment=HOME=/home/alice",
		"WantedBy=default.target",
	} {
		if !strings.Contains(p.Content, w) {
			t.Errorf("content missing %q\n%s", w, p.Content)
		}
	}
	if strings.Contains(p.Content, "User=") {
		t.Error("user unit must not set User=")
	}
	if got := cmdStrings(p.PostInstall); !contains(got, "loginctl enable-linger alice") {
		t.Errorf("PostInstall missing enable-linger: %v", got)
	}
}

func TestSystemdSystemUnit(t *testing.T) {
	p := systemd{}.InstallPlan(linuxConfig(true))
	if !p.NeedsRoot {
		t.Fatal("system plan must need root")
	}
	if p.UnitPath != "/etc/systemd/system/marshal.service" {
		t.Fatalf("UnitPath = %s", p.UnitPath)
	}
	if p.StagePath != "/home/alice/.marshal/marshal.service" {
		t.Fatalf("StagePath = %s", p.StagePath)
	}
	for _, w := range []string{"User=alice", "WantedBy=multi-user.target"} {
		if !strings.Contains(p.Content, w) {
			t.Errorf("content missing %q", w)
		}
	}
	if len(p.PostInstall) == 0 || p.PostInstall[0].String() != "sudo cp /home/alice/.marshal/marshal.service /etc/systemd/system/marshal.service" {
		t.Fatalf("first PostInstall = %v", p.PostInstall)
	}
}

func TestSystemdXDG(t *testing.T) {
	p := systemd{}.InstallPlan(linuxConfig(false))
	if strings.Contains(p.Content, "XDG_DATA_HOME") {
		t.Error("XDG_DATA_HOME must be omitted when empty")
	}
	c := linuxConfig(false)
	c.XDGData = "/data"
	if !strings.Contains(systemd{}.InstallPlan(c).Content, "Environment=XDG_DATA_HOME=/data") {
		t.Error("XDG_DATA_HOME must be present when set")
	}
}

func TestSystemdEnvQuoting(t *testing.T) {
	c := linuxConfig(false)
	c.Home = "/home/a b"
	if !strings.Contains(systemd{}.InstallPlan(c).Content, `Environment="HOME=/home/a b"`) {
		t.Error("env value with space must be quoted")
	}
}

// test helpers
func cmdStrings(cs []Cmd) []string {
	out := make([]string, len(cs))
	for i, c := range cs {
		out[i] = c.String()
	}
	return out
}

func contains(ss []string, want string) bool {
	for _, s := range ss {
		if s == want {
			return true
		}
	}
	return false
}
