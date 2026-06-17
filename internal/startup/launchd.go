package startup

import (
	"bytes"
	"encoding/xml"
	"fmt"
	"path/filepath"
	"strings"
)

type launchd struct{}

const launchdLabel = "com.marshal.daemon"

func (launchd) InstallPlan(c Config) Plan {
	content := renderLaunchdPlist(c)
	file := launchdLabel + ".plist"
	if c.System {
		unit := "/Library/LaunchDaemons/" + file
		stage := filepath.Join(c.StageDir, file)
		return Plan{
			UnitPath:  unit,
			StagePath: stage,
			Content:   content,
			NeedsRoot: true,
			Label:     launchdLabel,
			PostInstall: []Cmd{
				{"sudo", []string{"cp", stage, unit}},
				{"sudo", []string{"launchctl", "bootstrap", "system", unit}},
			},
			PostRemove: []Cmd{
				{"sudo", []string{"launchctl", "bootout", "system", unit}},
				{"sudo", []string{"rm", "-f", unit}},
			},
		}
	}
	unit := filepath.Join(c.Home, "Library", "LaunchAgents", file)
	domain := fmt.Sprintf("gui/%d", c.UID)
	return Plan{
		UnitPath:  unit,
		Content:   content,
		NeedsRoot: false,
		Label:     launchdLabel,
		PostInstall: []Cmd{
			{"launchctl", []string{"bootstrap", domain, unit}},
		},
		PostRemove: []Cmd{
			{"launchctl", []string{"bootout", domain, unit}},
		},
	}
}

// RemovePlan reuses InstallPlan; only UnitPath and PostRemove are meaningful to
// Remove (Content/PostInstall are ignored for uninstall).
func (l launchd) RemovePlan(c Config) Plan { return l.InstallPlan(c) }

func renderLaunchdPlist(c Config) string {
	var b strings.Builder
	b.WriteString(`<?xml version="1.0" encoding="UTF-8"?>` + "\n")
	b.WriteString(`<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">` + "\n")
	b.WriteString(`<plist version="1.0">` + "\n<dict>\n")
	stringElem(&b, "Label", launchdLabel)
	b.WriteString("  <key>ProgramArguments</key>\n  <array>\n")
	arrayString(&b, c.Binary)
	arrayString(&b, "daemon")
	b.WriteString("  </array>\n")
	boolElem(&b, "RunAtLoad", true)
	boolElem(&b, "KeepAlive", true)
	if c.System {
		stringElem(&b, "UserName", c.User)
	}
	b.WriteString("  <key>EnvironmentVariables</key>\n  <dict>\n")
	stringElem(&b, "HOME", c.Home)
	if c.XDGData != "" {
		stringElem(&b, "XDG_DATA_HOME", c.XDGData)
	}
	b.WriteString("  </dict>\n")
	b.WriteString("</dict>\n</plist>\n")
	return b.String()
}

func stringElem(b *strings.Builder, key, val string) {
	fmt.Fprintf(b, "  <key>%s</key>\n  <string>%s</string>\n", xmlEsc(key), xmlEsc(val))
}

func arrayString(b *strings.Builder, val string) {
	fmt.Fprintf(b, "    <string>%s</string>\n", xmlEsc(val))
}

func boolElem(b *strings.Builder, key string, v bool) {
	tag := "false"
	if v {
		tag = "true"
	}
	fmt.Fprintf(b, "  <key>%s</key>\n  <%s/>\n", xmlEsc(key), tag)
}

func xmlEsc(s string) string {
	var buf bytes.Buffer
	_ = xml.EscapeText(&buf, []byte(s))
	return buf.String()
}
