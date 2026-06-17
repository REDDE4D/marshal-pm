package startup

import (
	"strings"
	"testing"
)

func macConfig(system bool) Config {
	return Config{
		Binary:   "/usr/local/bin/marshal",
		User:     "alice",
		Home:     "/Users/alice",
		System:   system,
		StageDir: "/Users/alice/.marshal",
		UID:      501,
	}
}

func TestLaunchdUserPlist(t *testing.T) {
	p := launchd{}.InstallPlan(macConfig(false))
	if p.NeedsRoot {
		t.Fatal("user plan must not need root")
	}
	if p.UnitPath != "/Users/alice/Library/LaunchAgents/com.marshal.daemon.plist" {
		t.Fatalf("UnitPath = %s", p.UnitPath)
	}
	for _, w := range []string{
		"<string>/usr/local/bin/marshal</string>",
		"<string>daemon</string>",
		"<key>RunAtLoad</key>",
		"<key>KeepAlive</key>",
		"<key>HOME</key>",
		"<string>/Users/alice</string>",
	} {
		if !strings.Contains(p.Content, w) {
			t.Errorf("content missing %q\n%s", w, p.Content)
		}
	}
	if strings.Contains(p.Content, "UserName") {
		t.Error("user agent must not set UserName")
	}
	if got := cmdStrings(p.PostInstall); !contains(got, "launchctl bootstrap gui/501 /Users/alice/Library/LaunchAgents/com.marshal.daemon.plist") {
		t.Errorf("PostInstall missing bootstrap: %v", got)
	}
}

func TestLaunchdSystemPlist(t *testing.T) {
	p := launchd{}.InstallPlan(macConfig(true))
	if !p.NeedsRoot {
		t.Fatal("system plan must need root")
	}
	if p.UnitPath != "/Library/LaunchDaemons/com.marshal.daemon.plist" {
		t.Fatalf("UnitPath = %s", p.UnitPath)
	}
	if p.StagePath != "/Users/alice/.marshal/com.marshal.daemon.plist" {
		t.Fatalf("StagePath = %s", p.StagePath)
	}
	if !strings.Contains(p.Content, "<key>UserName</key>") {
		t.Error("system daemon must set UserName")
	}
}

func TestLaunchdXDGAndEscape(t *testing.T) {
	p := launchd{}.InstallPlan(macConfig(false))
	if strings.Contains(p.Content, "XDG_DATA_HOME") {
		t.Error("XDG_DATA_HOME must be omitted when empty")
	}
	c := macConfig(false)
	c.XDGData = "/d&d"
	out := launchd{}.InstallPlan(c).Content
	if !strings.Contains(out, "/d&amp;d") {
		t.Error("ampersand must be XML-escaped")
	}
	if strings.Contains(out, "/d&d<") || strings.Contains(out, ">/d&d") {
		t.Error("raw unescaped ampersand present")
	}
}
