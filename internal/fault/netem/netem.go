// Package netem builds the shell scripts that degrade a pod's network with
// Linux tc/netem. The scripts run inside an ephemeral container that shares the
// pod's network namespace and has the NET_ADMIN capability. The package depends
// on the standard library only; the scripts need POSIX sh, awk, grep, sleep and
// the tc and ip tools of iproute2 (see Dockerfile.netem).
//
// netem shapes traffic leaving the pod's interface, so latency and loss affect
// what the pod sends: responses to its callers and its own outgoing requests.
package netem

import (
	"fmt"
	"strings"
	"time"
)

// Container name prefixes. Names derive from the experiment UID, so injecting
// or reverting twice finds the container the first call added.
const (
	ContainerPrefix = "tremor-netem-"
	CleanupPrefix   = "tremor-netemoff-"
)

// ExitApplyFailed is the exit code of the apply script when tc could not set up
// the qdisc, typically because the node lacks the sch_netem kernel module or
// the container lacks NET_ADMIN. Any other non-zero code is not a netem error.
const ExitApplyFailed = 42

func suffix(experimentUID string) string {
	id := strings.ReplaceAll(experimentUID, "-", "")
	if len(id) > 8 {
		id = id[:8]
	}
	return id
}

// ContainerName is the name of the container that applies the fault.
func ContainerName(experimentUID string) string { return ContainerPrefix + suffix(experimentUID) }

// CleanupName is the name of the container that removes the fault early.
func CleanupName(experimentUID string) string { return CleanupPrefix + suffix(experimentUID) }

func seconds(d time.Duration) int {
	s := int(d.Round(time.Second) / time.Second)
	if s < 1 {
		s = 1
	}
	return s
}

// findDev picks the interface of the default route, falling back to eth0.
const findDev = `dev=$(ip route show default 2>/dev/null | awk '/^default/ {print $5; exit}')
[ -n "$dev" ] || dev=eth0
`

// LatencyScript delays every packet by latency, varying by up to jitter, for
// the given duration.
func LatencyScript(latency, jitter, d time.Duration) string {
	args := fmt.Sprintf("delay %dms", latency.Milliseconds())
	if jitter > 0 {
		args += fmt.Sprintf(" %dms", jitter.Milliseconds())
	}
	return applyScript(args, d)
}

// LossScript drops the given percentage of packets for the given duration.
func LossScript(percent int, d time.Duration) string {
	return applyScript(fmt.Sprintf("loss %d%%", percent), d)
}

// applyScript installs the qdisc, then watches it. It leaves early, removing
// the qdisc, when the duration is over or it is terminated, and also leaves if
// the qdisc was removed by CleanupScript. Failing to install exits with
// ExitApplyFailed.
func applyScript(netemArgs string, d time.Duration) string {
	return findDev + fmt.Sprintf(`cleanup() { tc qdisc del dev "$dev" root 2>/dev/null; }
if ! tc qdisc add dev "$dev" root netem %s; then
  echo "tremor-netem: tc could not add the netem qdisc (is the sch_netem kernel module loaded?)" >&2
  exit %d
fi
trap 'cleanup; exit 0' TERM INT
trap cleanup EXIT
i=0
while [ "$i" -lt %d ]; do
  sleep 1
  tc qdisc show dev "$dev" | grep -q netem || exit 0
  i=$((i + 1))
done
exit 0
`, netemArgs, ExitApplyFailed, seconds(d))
}

// CleanupScript removes the netem qdisc. The apply script notices within a
// second and exits. It is safe to run when there is nothing to remove.
func CleanupScript() string {
	return findDev + `tc qdisc del dev "$dev" root 2>/dev/null
exit 0
`
}
