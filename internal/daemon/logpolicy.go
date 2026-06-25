package daemon

import (
	"github.com/REDDE4D/marshal-pm/internal/config"
	"github.com/REDDE4D/marshal-pm/internal/logs"
)

// logPolicy resolves an app's effective log policy: the default with any
// per-app override fields applied.
func logPolicy(app config.App, def logs.Policy) logs.Policy {
	p := def
	if app.Logs == nil {
		return p
	}
	if app.Logs.MaxSizeMB != nil {
		p.MaxSizeMB = *app.Logs.MaxSizeMB
	}
	if app.Logs.MaxBackups != nil {
		p.MaxBackups = *app.Logs.MaxBackups
	}
	if app.Logs.MaxAgeDays != nil {
		p.MaxAgeDays = *app.Logs.MaxAgeDays
	}
	if app.Logs.Compress != nil {
		p.Compress = *app.Logs.Compress
	}
	return p
}
