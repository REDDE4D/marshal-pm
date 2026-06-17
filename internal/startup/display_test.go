package startup

import "testing"

func TestCmdDisplayShellQuotesSpaces(t *testing.T) {
	c := Cmd{Name: "sudo", Args: []string{"cp", "/home/a b/marshal.service", "/etc/x"}}
	got := c.Display()
	want := `sudo cp '/home/a b/marshal.service' /etc/x`
	if got != want {
		t.Fatalf("Display() = %q, want %q", got, want)
	}
}

func TestCmdStringStaysUnquoted(t *testing.T) {
	c := Cmd{Name: "systemctl", Args: []string{"--user", "enable"}}
	if c.String() != "systemctl --user enable" {
		t.Fatalf("String() = %q", c.String())
	}
}
