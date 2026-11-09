package stress

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestContainerName(t *testing.T) {
	if got := ContainerName("8bb4881f-1234-5678-9abc-def012345678"); got != "tremor-stress-8bb4881f" {
		t.Errorf("ContainerName = %q", got)
	}
	if got := ContainerName("abc"); got != "tremor-stress-abc" {
		t.Errorf("short uid: %q", got)
	}
	// Valid DNS label: lowercase alphanumerics and dashes, at most 63 chars.
	if n := ContainerName(strings.Repeat("f", 100)); len(n) > 63 {
		t.Errorf("name too long: %q", n)
	}
}

func TestScriptsContainParameters(t *testing.T) {
	cpu := CPUScript(4, 45*time.Second)
	if !strings.Contains(cpu, "-lt 4") || !strings.Contains(cpu, "sleep 45") {
		t.Errorf("CPU script missing parameters:\n%s", cpu)
	}
	mem := MemoryScript(64, 30*time.Second)
	if !strings.Contains(mem, "bs=67108864") || !strings.Contains(mem, "sleep 30") {
		t.Errorf("memory script missing parameters:\n%s", mem)
	}
	// Bad inputs are clamped instead of producing a broken script.
	if !strings.Contains(CPUScript(0, 0), "-lt 1") || !strings.Contains(CPUScript(0, 0), "sleep 1") {
		t.Error("CPU script should clamp workers and duration to 1")
	}
	if !strings.Contains(MemoryScript(0, 0), "bs=1048576") {
		t.Error("memory script should clamp to 1 MiB")
	}
}

func needSh(t *testing.T) string {
	t.Helper()
	sh, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("sh not available")
	}
	for _, tool := range []string{"yes", "sleep", "dd"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("%s not available", tool)
		}
	}
	return sh
}

// procs returns the PIDs of live processes whose command name is comm.
// Zombies are skipped: a killed process stays a zombie until its parent's
// parent reaps it, which some sandboxes' init never does.
func procs(t *testing.T, comm string) []int {
	t.Helper()
	entries, err := os.ReadDir("/proc")
	if err != nil {
		t.Skip("no /proc")
	}
	var pids []int
	for _, e := range entries {
		pid, err := strconv.Atoi(e.Name())
		if err != nil {
			continue
		}
		b, err := os.ReadFile(filepath.Join("/proc", e.Name(), "comm"))
		if err != nil || strings.TrimSpace(string(b)) != comm {
			continue
		}
		if st, err := os.ReadFile(filepath.Join("/proc", e.Name(), "status")); err == nil &&
			strings.Contains(string(st), "State:\tZ") {
			continue
		}
		pids = append(pids, pid)
	}
	return pids
}

func rssKiB(pid int) int {
	b, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/status")
	if err != nil {
		return 0
	}
	for _, line := range strings.Split(string(b), "\n") {
		if strings.HasPrefix(line, "VmRSS:") {
			f := strings.Fields(line)
			if len(f) >= 2 {
				n, _ := strconv.Atoi(f[1])
				return n
			}
		}
	}
	return 0
}

func TestCPUScriptRunsAndCleansUp(t *testing.T) {
	sh := needSh(t)
	before := len(procs(t, "yes"))

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, sh, "-c", CPUScript(2, 2*time.Second))
	start := time.Now()
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}

	time.Sleep(700 * time.Millisecond)
	if got := len(procs(t, "yes")) - before; got != 2 {
		t.Errorf("running yes workers = %d, want 2", got)
	}

	if err := cmd.Wait(); err != nil {
		t.Fatalf("script failed: %v", err)
	}
	if d := time.Since(start); d < 1800*time.Millisecond || d > 5*time.Second {
		t.Errorf("script took %s, want about 2s", d)
	}
	time.Sleep(200 * time.Millisecond)
	if got := len(procs(t, "yes")) - before; got != 0 {
		t.Errorf("%d yes worker(s) left running", got)
	}
}

func TestMemoryScriptHoldsMemoryThenReleases(t *testing.T) {
	sh := needSh(t)

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, sh, "-c", MemoryScript(64, 2*time.Second))
	start := time.Now()
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}

	time.Sleep(900 * time.Millisecond)
	var held int
	for _, pid := range procs(t, "dd") {
		if r := rssKiB(pid); r > held {
			held = r
		}
	}
	if held < 60*1024 {
		t.Errorf("dd holds %d KiB resident, want at least 60 MiB", held)
	}

	if err := cmd.Wait(); err != nil {
		t.Fatalf("script failed: %v", err)
	}
	if d := time.Since(start); d < 1800*time.Millisecond || d > 6*time.Second {
		t.Errorf("script took %s, want about 2s", d)
	}
	time.Sleep(200 * time.Millisecond)
	for _, pid := range procs(t, "dd") {
		if rssKiB(pid) > 60*1024 {
			t.Errorf("dd (pid %d) still holds memory after the script ended", pid)
		}
	}
}
