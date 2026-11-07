// Package version exposes build metadata injected at link time.
package version

// These are overridden with -ldflags "-X ..." in the Makefile and Dockerfile.
var (
	Version = "dev"
	Commit  = "none"
)

// String returns a human readable version line.
func String() string {
	return Version + " (" + Commit + ")"
}
