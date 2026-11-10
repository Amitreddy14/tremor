package netem

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

// fakeTools writes fake ip and tc programs into a directory and returns the
// environment to run scripts with. The fake tc keeps its "qdisc" as a file and
// logs every call.
func fakeTools(t *testing.T, failAdd bool) (env []string, state, logFile string) {
	t.Helper()
	// The scripts are POSIX sh; skip where there is none (plain Windows).
	for _, tool := range []string{"sh", "awk", "grep", "sleep"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("%s not found: the script tests need a POSIX shell", tool)
		}
	}
	dir := t.TempDir()
	state = filepath.Join(dir, "qdisc")
	logFile = filepath.Join(dir, "tc.log")

	write := func(name, body string) {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("#!/bin/sh\n"+body), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	write("ip", `echo "default via 10.0.0.1 dev eth9 proto dhcp"`)
	fail := "0"
	if failAdd {
		fail = "1"
	}
	write("tc", `echo "$*" >> "`+logFile+`"
case "$2" in
  add) [ "`+fail+`" = 1 ] && exit 2; echo "$*" > "`+state+`" ;;
  del) rm -f "`+state+`" ;;
  show) [ -f "`+state+`" ] && echo "qdisc netem 8001: root" ;;
esac
exit 0
`)
	return append(os.Environ(), "PATH="+dir+":"+os.Getenv("PATH")), state, logFile
}

func run(t *testing.T, env []string, script string) (int, string) {
	t.Helper()
	cmd := exec.Command("sh", "-c", script)
	cmd.Env = env
	out, err := cmd.CombinedOutput()
	var ee *exec.ExitError
	switch {
	case err == nil:
		return 0, string(out)
	case errors.As(err, &ee):
		return ee.ExitCode(), string(out)
	}
	t.Fatalf("running script: %v", err)
	return -1, ""
}

func readLog(t *testing.T, path string) string {
	t.Helper()
	b, _ := os.ReadFile(path)
	return string(b)
}

func TestContainerNames(t *testing.T) {
	uid := "0f1e2d3c-aaaa-bbbb-cccc-1234567890ab"
	if got := ContainerName(uid); got != "tremor-netem-0f1e2d3c" {
		t.Errorf("ContainerName = %q", got)
	}
	if got := CleanupName(uid); got != "tremor-netemoff-0f1e2d3c" {
		t.Errorf("CleanupName = %q", got)
	}
	if ContainerName("ab") != "tremor-netem-ab" {
		t.Error("short UIDs must be kept whole")
	}
	if len(CleanupName(uid)) > 63 {
		t.Error("container name too long")
	}
}

func TestScriptText(t *testing.T) {
	if s := LatencyScript(200*time.Millisecond, 20*time.Millisecond, 30*time.Second); !strings.Contains(s, "netem delay 200ms 20ms") || !strings.Contains(s, "-lt 30 ") {
		t.Errorf("latency script wrong:\n%s", s)
	}
	if s := LatencyScript(50*time.Millisecond, 0, time.Second); !strings.Contains(s, "netem delay 50ms;") || strings.Contains(s, "delay 50ms 0ms") {
		t.Errorf("zero jitter must be omitted:\n%s", s)
	}
	if s := LossScript(25, 0); !strings.Contains(s, "netem loss 25%;") || !strings.Contains(s, "-lt 1 ") {
		t.Errorf("loss script wrong (and duration must be at least 1s):\n%s", s)
	}
	if !strings.Contains(LossScript(5, time.Second), "exit 42") {
		t.Error("apply failure must exit 42")
	}
}

func TestApplyRunsToCompletionAndCleansUp(t *testing.T) {
	env, state, logFile := fakeTools(t, false)
	start := time.Now()
	code, out := run(t, env, LatencyScript(200*time.Millisecond, 20*time.Millisecond, 2*time.Second))
	if code != 0 {
		t.Fatalf("exit %d: %s", code, out)
	}
	if time.Since(start) < 2*time.Second {
		t.Error("script ended before its duration")
	}
	log := readLog(t, logFile)
	if !strings.Contains(log, "qdisc add dev eth9 root netem delay 200ms 20ms") {
		t.Errorf("did not add the qdisc on the default-route device:\n%s", log)
	}
	if !strings.Contains(log, "qdisc del dev eth9 root") {
		t.Errorf("did not remove the qdisc:\n%s", log)
	}
	if _, err := os.Stat(state); err == nil {
		t.Error("qdisc still present after the script ended")
	}
}

func TestApplyFailureExits42(t *testing.T) {
	env, _, _ := fakeTools(t, true)
	code, out := run(t, env, LossScript(10, 5*time.Second))
	if code != ExitApplyFailed {
		t.Fatalf("exit %d, want %d (%s)", code, ExitApplyFailed, out)
	}
	if !strings.Contains(out, "sch_netem") {
		t.Errorf("message should mention the kernel module: %q", out)
	}
}

func startApply(t *testing.T, env []string, script string) (*exec.Cmd, chan error) {
	t.Helper()
	cmd := exec.Command("sh", "-c", script)
	cmd.Env = env
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	return cmd, done
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func exists(path string) func() bool {
	return func() bool { _, err := os.Stat(path); return err == nil }
}

func TestCleanupStopsApplyEarly(t *testing.T) {
	env, state, _ := fakeTools(t, false)
	_, done := startApply(t, env, LossScript(30, 60*time.Second))
	waitFor(t, "qdisc to be installed", exists(state))

	if code, out := run(t, env, CleanupScript()); code != 0 {
		t.Fatalf("cleanup exit %d: %s", code, out)
	}
	if exists(state)() {
		t.Fatal("cleanup left the qdisc in place")
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("apply script exited with %v, want 0", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("apply script did not notice the cleanup")
	}
}

func TestCleanupWithNothingToRemove(t *testing.T) {
	env, _, _ := fakeTools(t, false)
	if code, out := run(t, env, CleanupScript()); code != 0 {
		t.Fatalf("exit %d: %s", code, out)
	}
}

func TestTerminationRemovesQdisc(t *testing.T) {
	env, state, _ := fakeTools(t, false)
	cmd, done := startApply(t, env, LatencyScript(100*time.Millisecond, 0, 60*time.Second))
	waitFor(t, "qdisc to be installed", exists(state))

	if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("exit after SIGTERM: %v, want clean exit", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("script ignored SIGTERM")
	}
	if exists(state)() {
		t.Fatal("SIGTERM left the qdisc in place")
	}
}
