package version

import "testing"

func TestString(t *testing.T) {
	Version, Commit = "v0.0.1", "abc123"
	t.Cleanup(func() { Version, Commit = "dev", "none" })

	if got, want := String(), "v0.0.1 (abc123)"; got != want {
		t.Fatalf("String() = %q, want %q", got, want)
	}
}
