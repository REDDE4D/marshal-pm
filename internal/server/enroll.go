package server

// enrollMinter adapts the running server's AuthStore, cert fingerprint, and
// fleet listen address to the dashboard.EnrollMinter interface. Rotating goes
// through the in-memory AuthStore, so a minted token is immediately effective
// for enrollment (no restart needed, unlike the CLI on-disk rotate path).
type enrollMinter struct {
	auth      *AuthStore
	fp        string
	fleetAddr string
}

func (m enrollMinter) RotateEnrollToken() (string, error) { return m.auth.rotate("enroll") }
func (m enrollMinter) Fingerprint() string                { return m.fp }
func (m enrollMinter) FleetAddress() string               { return m.fleetAddr }
