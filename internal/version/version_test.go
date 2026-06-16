package version

import "testing"

func TestString(t *testing.T) {
	if String() == "" {
		t.Fatal("version string must not be empty")
	}
}
