package startup

import (
	"fmt"
	"path/filepath"
	"strings"
)

type systemd struct{}

func (systemd) InstallPlan(c Config) Plan {
	content := renderSystemdUnit(c)
	if c.System {
		unit := "/etc/systemd/system/marshal.service"
		stage := filepath.Join(c.StageDir, "marshal.service")
		return Plan{
			UnitPath:  unit,
			StagePath: stage,
			Content:   content,
			NeedsRoot: true,
			Label:     "marshal.service",
			PostInstall: []Cmd{
				{"sudo", []string{"cp", stage, unit}},
				{"sudo", []string{"systemctl", "daemon-reload"}},
				{"sudo", []string{"systemctl", "enable", "--now", "marshal.service"}},
			},
			PostRemove: []Cmd{
				{"sudo", []string{"systemctl", "disable", "--now", "marshal.service"}},
				{"sudo", []string{"rm", "-f", unit}},
			},
		}
	}
	unit := filepath.Join(c.Home, ".config", "systemd", "user", "marshal.service")
	return Plan{
		UnitPath:  unit,
		Content:   content,
		NeedsRoot: false,
		Label:     "marshal.service",
		PostInstall: []Cmd{
			{"systemctl", []string{"--user", "daemon-reload"}},
			{"systemctl", []string{"--user", "enable", "--now", "marshal.service"}},
			{"loginctl", []string{"enable-linger", c.User}},
		},
		PostRemove: []Cmd{
			{"systemctl", []string{"--user", "disable", "--now", "marshal.service"}},
		},
	}
}

func (s systemd) RemovePlan(c Config) Plan { return s.InstallPlan(c) }

func renderSystemdUnit(c Config) string {
	var b strings.Builder
	b.WriteString("[Unit]\n")
	b.WriteString("Description=Marshal process manager\n")
	b.WriteString("After=network.target\n\n")
	b.WriteString("[Service]\n")
	b.WriteString("Type=simple\n")
	fmt.Fprintf(&b, "ExecStart=%s daemon\n", c.Binary)
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
	if strings.ContainsAny(val, " \t") {
		return fmt.Sprintf("Environment=\"%s=%s\"\n", key, val)
	}
	return fmt.Sprintf("Environment=%s=%s\n", key, val)
}
