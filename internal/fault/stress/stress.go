// Package stress builds the shell scripts that CPU and memory stress run inside
// an ephemeral container. It depends on the standard library only, and the
// scripts use only POSIX sh plus yes, sleep, kill and dd, all present in
// BusyBox, so the default image can be tiny.
package stress

import (
	"fmt"
	"strings"
	"time"
)

// ContainerPrefix starts the name of every stress container Tremor adds.
const ContainerPrefix = "tremor-stress-"

// ContainerName returns the ephemeral container name for an experiment. It is
// derived from the experiment UID, so injecting twice finds the first one.
func ContainerName(experimentUID string) string {
	id := strings.ReplaceAll(experimentUID, "-", "")
	if len(id) > 8 {
		id = id[:8]
	}
	return ContainerPrefix + id
}

func seconds(d time.Duration) int {
	s := int(d.Round(time.Second) / time.Second)
	if s < 1 {
		s = 1
	}
	return s
}

// CPUScript burns CPU with workers busy loops for the given duration, then
// stops them. The container exits when the script ends.
func CPUScript(workers int, d time.Duration) string {
	if workers < 1 {
		workers = 1
	}
	return fmt.Sprintf(`pids=""
i=0
while [ "$i" -lt %d ]; do
  yes > /dev/null &
  pids="$pids $!"
  i=$((i + 1))
done
sleep %d
kill $pids 2>/dev/null
exit 0
`, workers, seconds(d))
}

// MemoryScript holds mib MiB of resident memory for the given duration. dd
// fills one buffer of that size from /dev/zero, then blocks writing it into a
// pipe that nothing reads, so the buffer stays allocated until sleep exits.
func MemoryScript(mib int, d time.Duration) string {
	if mib < 1 {
		mib = 1
	}
	bytes := int64(mib) * 1024 * 1024
	return fmt.Sprintf(`dd if=/dev/zero bs=%d count=1 2>/dev/null | sleep %d
exit 0
`, bytes, seconds(d))
}
