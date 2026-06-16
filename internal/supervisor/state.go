package supervisor

// State is the lifecycle state of a supervised instance.
type State string

const (
	StateStarting   State = "starting"
	StateOnline     State = "online"
	StateStopping   State = "stopping"
	StateStopped    State = "stopped"
	StateRestarting State = "restarting"
	StateErrored    State = "errored"
)
