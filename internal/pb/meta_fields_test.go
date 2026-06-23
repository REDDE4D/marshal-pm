package pb

import "testing"

func TestHelloMetaFields(t *testing.T) {
	h := &Hello{Hostname: "web-01", Os: "linux", Arch: "amd64", HostBootUnix: 1700000000}
	if h.GetHostname() != "web-01" || h.GetOs() != "linux" || h.GetArch() != "amd64" || h.GetHostBootUnix() != 1700000000 {
		t.Fatalf("Hello meta getters wrong: %+v", h)
	}
}

func TestAgentStateMetaFields(t *testing.T) {
	a := &AgentState{Hostname: "web-01", Ip: "203.0.113.7", Os: "linux", Arch: "amd64", MarshalVersion: "v0.1.0", HostBootUnix: 1700000000}
	if a.GetIp() != "203.0.113.7" || a.GetMarshalVersion() != "v0.1.0" || a.GetHostBootUnix() != 1700000000 {
		t.Fatalf("AgentState meta getters wrong: %+v", a)
	}
}
