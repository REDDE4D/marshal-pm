package startup

import (
	"fmt"
	"path/filepath"
	"strings"
)

type systemd struct{}

func (systemd) InstallPlan(c Config) Plan {
	content := renderSystemdUnit(c)
	svc := c.systemdUnit() + ".service"
	if c.System {
		unit := "/etc/systemd/system/" + svc
		stage := filepath.Join(c.StageDir, svc)
		return Plan{
			UnitPath:  unit,
			StagePath: stage,
			Content:   content,
			NeedsRoot: true,
			Label:     svc,
			PostInstall: []Cmd{
				{"sudo", []string{"cp", stage, unit}},
				{"sudo", []string{"systemctl", "daemon-reload"}},
				{"sudo", []string{"systemctl", "enable", "--now", svc}},
			},
			PostRemove: []Cmd{
				{"sudo", []string{"systemctl", "disable", "--now", svc}},
				{"sudo", []string{"rm", "-f", unit}},
			},
		}
	}
	unit := filepath.Join(c.Home, ".config", "systemd", "user", svc)
	return Plan{
		UnitPath:  unit,
		Content:   content,
		NeedsRoot: false,
		Label:     svc,
		PostInstall: []Cmd{
			{"systemctl", []string{"--user", "daemon-reload"}},
			{"systemctl", []string{"--user", "enable", "--now", svc}},
			{"loginctl", []string{"enable-linger", c.User}},
		},
		PostRemove: []Cmd{
			{"systemctl", []string{"--user", "disable", "--now", svc}},
		},
	}
}

// RemovePlan reuses InstallPlan; only UnitPath and PostRemove are meaningful to
// Remove (Content/PostInstall are ignored for uninstall).
func (s systemd) RemovePlan(c Config) Plan { return s.InstallPlan(c) }

func renderSystemdUnit(c Config) string {
	var b strings.Builder
	b.WriteString("[Unit]\n")
	fmt.Fprintf(&b, "Description=Marshal (%s)\n", strings.Join(c.args(), " "))
	b.WriteString("After=network.target\n\n")
	b.WriteString("[Service]\n")
	b.WriteString("Type=simple\n")
	fmt.Fprintf(&b, "ExecStart=%s %s\n", c.Binary, strings.Join(c.args(), " "))
	b.WriteString("Restart=on-failure\n")
	if c.System {
		fmt.Fprintf(&b, "User=%s\n", c.User)
	}
	b.WriteString(systemdEnv("HOME", c.Home))
	if c.XDGData != "" {
		b.WriteString(systemdEnv("XDG_DATA_HOME", c.XDGData))
	}
	b.WriteString("\n[Install]\n")
	if c.System {
		b.WriteString("WantedBy=multi-user.target\n")
	} else {
		b.WriteString("WantedBy=default.target\n")
	}
	return b.String()
}

func systemdEnv(key, val string) string {
	if strings.ContainsAny(val, " \t\"\\") {
		r := strings.NewReplacer(`\`, `\\`, `"`, `\"`)
		return fmt.Sprintf("Environment=\"%s=%s\"\n", key, r.Replace(val))
	}
	return fmt.Sprintf("Environment=%s=%s\n", key, val)
}
