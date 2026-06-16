// Package version exposes the Marshal build version.
package version

// Version is overridden at build time via -ldflags.
var Version = "0.0.0-dev"

// String returns the current version string.
func String() string { return Version }
