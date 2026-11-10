package controller

import (
	"context"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	policyv1 "k8s.io/api/policy/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	tremorv1 "github.com/Amitreddy14/tremor/api/v1alpha1"
	"github.com/Amitreddy14/tremor/internal/fault"
	"github.com/Amitreddy14/tremor/internal/fault/netem"
)

func networkExperiment(name, app string, typ tremorv1.FaultType, p tremorv1.FaultParams) *tremorv1.ChaosExperiment {
	exp := newExperiment(name, app)
	exp.Spec.Fault = tremorv1.FaultSpec{Type: typ, Params: p}
	return exp
}

func setEphemeralState(t *testing.T, c client.Client, key types.NamespacedName, name string, state corev1.ContainerState) {
	t.Helper()
	var pod corev1.Pod
	if err := c.Get(context.Background(), key, &pod); err != nil {
		t.Fatal(err)
	}
	pod.Status.EphemeralContainerStatuses = []corev1.ContainerStatus{{Name: name, State: state}}
	if err := c.Status().Update(context.Background(), &pod); err != nil {
		t.Fatal(err)
	}
}

// holdingNetemPod creates three ready pods, starts the experiment and waits
// until it is Holding, returning the single affected pod.
func holdingNetemPod(t *testing.T, c client.Client, exp *tremorv1.ChaosExperiment, app string) (types.NamespacedName, tremorv1.ChaosExperiment) {
	t.Helper()
	for i := 0; i < 3; i++ {
		newReadyPod(t, c, app)
	}
	if err := c.Create(context.Background(), exp); err != nil {
		t.Fatal(err)
	}
	eventually(t, 30*time.Second, "Holding phase", func() bool {
		return phaseOf(c, exp.Name) == tremorv1.PhaseHolding
	})
	run := getExperiment(t, c, exp.Name)
	if len(run.Status.AffectedPods) != 1 {
		t.Fatalf("affected pods = %d, want 1", len(run.Status.AffectedPods))
	}
	return types.NamespacedName{Namespace: testNamespace, Name: run.Status.AffectedPods[0].Name}, run
}

func TestNetworkFaults(t *testing.T) {
	tests := []struct {
		name   string
		app    string
		fault  tremorv1.FaultType
		params tremorv1.FaultParams
		script string
	}{
		{
			"NetworkLatency", "slow-net", tremorv1.FaultNetworkLatency,
			tremorv1.FaultParams{Count: 1, Latency: &metav1.Duration{Duration: 300 * time.Millisecond},
				Jitter: &metav1.Duration{Duration: 50 * time.Millisecond}, Duration: &metav1.Duration{Duration: 2 * time.Second}},
			"netem delay 300ms 50ms",
		},
		{
			"NetworkLoss", "lossy-net", tremorv1.FaultNetworkLoss,
			tremorv1.FaultParams{Count: 1, LossPercent: 40, Duration: &metav1.Duration{Duration: 2 * time.Second}},
			"netem loss 40%",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := startEnv(t)
			ctx := context.Background()
			exp := networkExperiment(tt.app+"-run", tt.app, tt.fault, tt.params)
			key, run := holdingNetemPod(t, c, exp, tt.app)

			var pod corev1.Pod
			if err := c.Get(ctx, key, &pod); err != nil {
				t.Fatal(err)
			}
			name := netem.ContainerName(string(run.UID))
			if len(pod.Spec.EphemeralContainers) != 1 || pod.Spec.EphemeralContainers[0].Name != name {
				t.Fatalf("ephemeral containers = %+v, want one named %s", pod.Spec.EphemeralContainers, name)
			}
			ec := pod.Spec.EphemeralContainers[0]
			if ec.Image != "netem:test" || len(ec.Command) != 3 || !strings.Contains(ec.Command[2], tt.script) {
				t.Errorf("container image/command wrong: %s %v", ec.Image, ec.Command)
			}
			sc := ec.SecurityContext
			if sc == nil || sc.Capabilities == nil || len(sc.Capabilities.Add) != 1 || sc.Capabilities.Add[0] != "NET_ADMIN" {
				t.Errorf("netem container needs exactly NET_ADMIN: %+v", sc)
			}

			// The kubelet reports it running; past its 2s duration the experiment
			// still waits for the container, not a timer.
			setEphemeralState(t, c, key, name, corev1.ContainerState{Running: &corev1.ContainerStateRunning{}})
			time.Sleep(3 * time.Second)
			if p := phaseOf(c, exp.Name); p != tremorv1.PhaseHolding {
				t.Fatalf("phase = %s while the netem container still runs, want Holding", p)
			}

			setEphemeralState(t, c, key, name, corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{ExitCode: 0}})
			eventually(t, 30*time.Second, "Completed phase", func() bool {
				return phaseOf(c, exp.Name) == tremorv1.PhaseCompleted
			})

			// A fault that ended by itself needs no cleanup container.
			if err := c.Get(ctx, key, &pod); err != nil {
				t.Fatal(err)
			}
			if len(pod.Spec.EphemeralContainers) != 1 {
				t.Errorf("ephemeral containers = %d after a normal end, want 1", len(pod.Spec.EphemeralContainers))
			}
			done := getExperiment(t, c, exp.Name)
			if done.Status.RevertedAt == nil || done.Status.PodRecoveryMillis == nil {
				t.Fatalf("missing release or recovery: %+v", done.Status)
			}
			var list corev1.PodList
			if err := c.List(ctx, &list, client.InNamespace(testNamespace), client.MatchingLabels{"app": tt.app}); err != nil {
				t.Fatal(err)
			}
			if len(list.Items) != 3 {
				t.Errorf("network faults must not remove pods: %d left, want 3", len(list.Items))
			}
		})
	}
}

func TestNetemApplyFailureFails(t *testing.T) {
	c := startEnv(t)
	exp := networkExperiment("netem-broken", "netem-broken", tremorv1.FaultNetworkLoss,
		tremorv1.FaultParams{Count: 1, LossPercent: 10, Duration: &metav1.Duration{Duration: 2 * time.Second}})
	key, run := holdingNetemPod(t, c, exp, "netem-broken")

	setEphemeralState(t, c, key, netem.ContainerName(string(run.UID)), corev1.ContainerState{
		Terminated: &corev1.ContainerStateTerminated{ExitCode: netem.ExitApplyFailed, Message: "RTNETLINK answers: No such file or directory"},
	})
	eventually(t, 30*time.Second, "Failed phase", func() bool {
		return phaseOf(c, exp.Name) == tremorv1.PhaseFailed
	})
	if msg := getExperiment(t, c, exp.Name).Status.Message; !strings.Contains(msg, "sch_netem") {
		t.Errorf("message = %q, want it to point at the sch_netem module", msg)
	}
}

func TestAbortWhileNetemActiveStartsCleanup(t *testing.T) {
	c := startEnv(t)
	ctx := context.Background()
	exp := networkExperiment("netem-abort", "netem-abort", tremorv1.FaultNetworkLatency,
		tremorv1.FaultParams{Count: 1, Duration: &metav1.Duration{Duration: time.Minute}})
	key, run := holdingNetemPod(t, c, exp, "netem-abort")
	setEphemeralState(t, c, key, netem.ContainerName(string(run.UID)), corev1.ContainerState{Running: &corev1.ContainerStateRunning{}})

	var cur tremorv1.ChaosExperiment
	if err := c.Get(ctx, client.ObjectKeyFromObject(exp), &cur); err != nil {
		t.Fatal(err)
	}
	if cur.Annotations == nil {
		cur.Annotations = map[string]string{}
	}
	cur.Annotations[tremorv1.AbortAnnotation] = "true"
	if err := c.Update(ctx, &cur); err != nil {
		t.Fatal(err)
	}
	eventually(t, 30*time.Second, "Aborted phase", func() bool {
		return phaseOf(c, exp.Name) == tremorv1.PhaseAborted
	})

	var pod corev1.Pod
	if err := c.Get(ctx, key, &pod); err != nil {
		t.Fatal(err)
	}
	if !hasContainer(&pod, netem.CleanupName(string(run.UID))) {
		t.Errorf("no cleanup container after abort: %+v", pod.Spec.EphemeralContainers)
	}
}

func hasContainer(pod *corev1.Pod, name string) bool {
	for _, e := range pod.Spec.EphemeralContainers {
		if e.Name == name {
			return true
		}
	}
	return false
}

// --- NodeDrain ---

func newNode(t *testing.T, c client.Client, name string) {
	t.Helper()
	if err := c.Create(context.Background(), &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: name}}); err != nil {
		t.Fatal(err)
	}
}

func getNode(t *testing.T, c client.Client, name string) corev1.Node {
	t.Helper()
	var n corev1.Node
	if err := c.Get(context.Background(), types.NamespacedName{Name: name}, &n); err != nil {
		t.Fatal(err)
	}
	return n
}

// newReadyPodOn is newReadyPod for a pod already bound to a node.
func newReadyPodOn(t *testing.T, c client.Client, app, node string) *corev1.Pod {
	t.Helper()
	ctx := context.Background()
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{GenerateName: app + "-", Namespace: testNamespace, Labels: map[string]string{"app": app}},
		Spec:       corev1.PodSpec{NodeName: node, Containers: []corev1.Container{{Name: "c", Image: "busybox"}}},
	}
	if err := c.Create(ctx, pod); err != nil {
		t.Fatalf("creating pod: %v", err)
	}
	pod.Status.Phase = corev1.PodRunning
	pod.Status.Conditions = []corev1.PodCondition{{Type: corev1.PodReady, Status: corev1.ConditionTrue}}
	if err := c.Status().Update(ctx, pod); err != nil {
		t.Fatalf("marking pod ready: %v", err)
	}
	return pod
}

func drainExperiment(name, app string) *tremorv1.ChaosExperiment {
	exp := newExperiment(name, app)
	exp.Spec.Fault = tremorv1.FaultSpec{Type: tremorv1.FaultNodeDrain}
	exp.Spec.Safety.MaxAffectedPercent = 100
	return exp
}

func nodePodCount(t *testing.T, c client.Client, app, node string) int {
	t.Helper()
	var list corev1.PodList
	if err := c.List(context.Background(), &list, client.InNamespace(testNamespace), client.MatchingLabels{"app": app}); err != nil {
		t.Fatal(err)
	}
	n := 0
	for i := range list.Items {
		if list.Items[i].Spec.NodeName == node && list.Items[i].DeletionTimestamp.IsZero() {
			n++
		}
	}
	return n
}

func TestNodeDrainLifecycle(t *testing.T) {
	c := startEnv(t)
	ctx := context.Background()
	newNode(t, c, "drain-a")
	newNode(t, c, "drain-b")
	newReadyPodOn(t, c, "drained", "drain-a")
	newReadyPodOn(t, c, "drained", "drain-a")
	newReadyPodOn(t, c, "drained", "drain-b")

	exp := drainExperiment("drain-run", "drained")
	if err := c.Create(ctx, exp); err != nil {
		t.Fatal(err)
	}

	// The node with the most pods is cordoned and its pods are evicted.
	eventually(t, 30*time.Second, "Recovering phase", func() bool {
		return phaseOf(c, "drain-run") == tremorv1.PhaseRecovering
	})
	run := getExperiment(t, c, "drain-run")
	if run.Status.Node != "drain-a" || len(run.Status.AffectedPods) != 2 {
		t.Fatalf("node = %q, affected = %d; want drain-a with 2 pods", run.Status.Node, len(run.Status.AffectedPods))
	}
	n := getNode(t, c, "drain-a")
	if !n.Spec.Unschedulable || n.Annotations[fault.AnnotationNodeHeldBy] != string(run.UID) || n.Annotations[fault.AnnotationWasUnschedulable] != "false" {
		t.Errorf("node not cordoned and marked: unschedulable=%v annotations=%v", n.Spec.Unschedulable, n.Annotations)
	}
	if got := nodePodCount(t, c, "drained", "drain-a"); got != 0 {
		t.Errorf("%d pod(s) left on the drained node, want 0", got)
	}
	if got := nodePodCount(t, c, "drained", "drain-b"); got != 1 {
		t.Errorf("%d pod(s) on the other node, want 1 (untouched)", got)
	}

	// Play the scheduler: replacements land on the node that is still open.
	newReadyPodOn(t, c, "drained", "drain-b")
	newReadyPodOn(t, c, "drained", "drain-b")
	eventually(t, 30*time.Second, "Completed phase", func() bool {
		return phaseOf(c, "drain-run") == tremorv1.PhaseCompleted
	})

	// The node is handed back exactly as it was.
	n = getNode(t, c, "drain-a")
	if n.Spec.Unschedulable || len(n.Annotations) != 0 {
		t.Errorf("node not restored: unschedulable=%v annotations=%v", n.Spec.Unschedulable, n.Annotations)
	}
	done := getExperiment(t, c, "drain-run")
	if done.Status.PodRecoveryMillis == nil {
		t.Fatalf("no pod recovery recorded: %+v", done.Status)
	}
	// Recovery runs from the eviction, not from the uncordon at the end.
	if done.Status.PodsRecoveredAt == nil || done.Status.InjectedAt == nil ||
		done.Status.PodsRecoveredAt.Time.Before(done.Status.InjectedAt.Time) {
		t.Errorf("recovery timestamps inconsistent: %+v", done.Status)
	}

	eventually(t, 10*time.Second, "run in history", func() bool {
		r, err := testHistory.Get(ctx, string(done.UID))
		if err != nil {
			return false
		}
		_, active := r.FaultActive()
		return r.FaultType == "NodeDrain" && r.Result == "completed" && !active
	})
}

func TestNodeDrainRestoresAnAlreadyCordonedNode(t *testing.T) {
	c := startEnv(t)
	ctx := context.Background()
	if err := c.Create(ctx, &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "drain-a-pre"}, Spec: corev1.NodeSpec{Unschedulable: true}}); err != nil {
		t.Fatal(err)
	}
	newNode(t, c, "drain-b-open")
	newReadyPodOn(t, c, "precordoned", "drain-a-pre")
	newReadyPodOn(t, c, "precordoned", "drain-b-open")

	exp := drainExperiment("drain-pre-run", "precordoned")
	if err := c.Create(ctx, exp); err != nil {
		t.Fatal(err)
	}
	eventually(t, 30*time.Second, "Recovering phase", func() bool {
		return phaseOf(c, exp.Name) == tremorv1.PhaseRecovering
	})
	if n := getNode(t, c, "drain-a-pre"); n.Annotations[fault.AnnotationWasUnschedulable] != "true" {
		t.Fatalf("was-unschedulable = %q, want true", n.Annotations[fault.AnnotationWasUnschedulable])
	}

	abortExperiment(t, c, exp.Name)
	eventually(t, 30*time.Second, "Aborted phase", func() bool {
		return phaseOf(c, exp.Name) == tremorv1.PhaseAborted
	})
	// Abort must not leave the node cordoned by us, but it was cordoned before.
	n := getNode(t, c, "drain-a-pre")
	if !n.Spec.Unschedulable || len(n.Annotations) != 0 {
		t.Errorf("node should be back to its own cordon without our markers: unschedulable=%v annotations=%v", n.Spec.Unschedulable, n.Annotations)
	}
}

func abortExperiment(t *testing.T, c client.Client, name string) {
	t.Helper()
	var cur tremorv1.ChaosExperiment
	if err := c.Get(context.Background(), types.NamespacedName{Namespace: testNamespace, Name: name}, &cur); err != nil {
		t.Fatal(err)
	}
	if cur.Annotations == nil {
		cur.Annotations = map[string]string{}
	}
	cur.Annotations[tremorv1.AbortAnnotation] = "true"
	if err := c.Update(context.Background(), &cur); err != nil {
		t.Fatal(err)
	}
}

func TestAbortedNodeDrainUncordons(t *testing.T) {
	c := startEnv(t)
	newNode(t, c, "drain-abort-a")
	newNode(t, c, "drain-abort-b")
	newReadyPodOn(t, c, "drain-abort", "drain-abort-a")
	newReadyPodOn(t, c, "drain-abort", "drain-abort-a")
	newReadyPodOn(t, c, "drain-abort", "drain-abort-b")

	exp := drainExperiment("drain-abort-run", "drain-abort")
	if err := c.Create(context.Background(), exp); err != nil {
		t.Fatal(err)
	}
	eventually(t, 30*time.Second, "Recovering phase", func() bool {
		return phaseOf(c, exp.Name) == tremorv1.PhaseRecovering
	})
	if !getNode(t, c, "drain-abort-a").Spec.Unschedulable {
		t.Fatal("node should be cordoned while the drain is running")
	}
	abortExperiment(t, c, exp.Name)
	eventually(t, 30*time.Second, "Aborted phase", func() bool {
		return phaseOf(c, exp.Name) == tremorv1.PhaseAborted
	})
	if n := getNode(t, c, "drain-abort-a"); n.Spec.Unschedulable || len(n.Annotations) != 0 {
		t.Errorf("abort left the node cordoned: unschedulable=%v annotations=%v", n.Spec.Unschedulable, n.Annotations)
	}
}

func TestDeletingExperimentUncordonsNode(t *testing.T) {
	c := startEnv(t)
	newNode(t, c, "drain-del-a")
	newNode(t, c, "drain-del-b")
	newReadyPodOn(t, c, "drain-del", "drain-del-a")
	newReadyPodOn(t, c, "drain-del", "drain-del-a")
	newReadyPodOn(t, c, "drain-del", "drain-del-b")

	exp := drainExperiment("drain-del-run", "drain-del")
	if err := c.Create(context.Background(), exp); err != nil {
		t.Fatal(err)
	}
	eventually(t, 30*time.Second, "Recovering phase", func() bool {
		return phaseOf(c, exp.Name) == tremorv1.PhaseRecovering
	})
	if err := c.Delete(context.Background(), exp); err != nil {
		t.Fatal(err)
	}
	eventually(t, 30*time.Second, "node uncordoned and experiment gone", func() bool {
		var cur tremorv1.ChaosExperiment
		err := c.Get(context.Background(), client.ObjectKeyFromObject(exp), &cur)
		n := getNode(t, c, "drain-del-a")
		return apierrors.IsNotFound(err) && !n.Spec.Unschedulable
	})
}

func TestNodeDrainBlockedByBudget(t *testing.T) {
	c := startEnv(t)
	ctx := context.Background()
	newNode(t, c, "drain-pdb-a")
	newNode(t, c, "drain-pdb-b")
	newReadyPodOn(t, c, "drain-pdb", "drain-pdb-a")
	newReadyPodOn(t, c, "drain-pdb", "drain-pdb-a")
	newReadyPodOn(t, c, "drain-pdb", "drain-pdb-b")

	// No disruption controller runs in envtest, so the budget allows none.
	zero := intstr.FromInt32(0)
	pdb := &policyv1.PodDisruptionBudget{
		ObjectMeta: metav1.ObjectMeta{Name: "drain-pdb", Namespace: testNamespace},
		Spec: policyv1.PodDisruptionBudgetSpec{
			MaxUnavailable: &zero,
			Selector:       &metav1.LabelSelector{MatchLabels: map[string]string{"app": "drain-pdb"}},
		},
	}
	if err := c.Create(ctx, pdb); err != nil {
		t.Fatal(err)
	}

	exp := drainExperiment("drain-pdb-run", "drain-pdb")
	if err := c.Create(ctx, exp); err != nil {
		t.Fatal(err)
	}
	eventually(t, 30*time.Second, "Failed phase", func() bool {
		return phaseOf(c, exp.Name) == tremorv1.PhaseFailed
	})
	if msg := getExperiment(t, c, exp.Name).Status.Message; !strings.Contains(msg, "eviction blocked") {
		t.Errorf("message = %q, want it to say the eviction was blocked", msg)
	}
	// The node ends up open again. The controller reads experiments from a cache
	// that can lag a moment behind its own status write, so a reconcile queued
	// during the refused eviction may cordon once more before it notices the run
	// has failed; it releases the node again straight away, so wait for that.
	eventually(t, 30*time.Second, "node released after the refused drain", func() bool {
		n := getNode(t, c, "drain-pdb-a")
		return !n.Spec.Unschedulable && len(n.Annotations) == 0
	})
}

func TestNodeDrainNeedsAnotherNode(t *testing.T) {
	c := startEnv(t)
	newNode(t, c, "drain-lonely")
	newReadyPodOn(t, c, "drain-lonely", "drain-lonely")
	newReadyPodOn(t, c, "drain-lonely", "drain-lonely")

	exp := drainExperiment("drain-lonely-run", "drain-lonely")
	if err := c.Create(context.Background(), exp); err != nil {
		t.Fatal(err)
	}
	eventually(t, 30*time.Second, "Failed phase", func() bool {
		return phaseOf(c, exp.Name) == tremorv1.PhaseFailed
	})
	if msg := getExperiment(t, c, exp.Name).Status.Message; !strings.Contains(msg, "no node can be drained") {
		t.Errorf("message = %q, want the safety refusal", msg)
	}
	if n := getNode(t, c, "drain-lonely"); n.Spec.Unschedulable {
		t.Error("a refused drain must not cordon the node")
	}
}

func TestNodeDrainDryRun(t *testing.T) {
	c := startEnv(t)
	newNode(t, c, "drain-dry-a")
	newNode(t, c, "drain-dry-b")
	newReadyPodOn(t, c, "drain-dry", "drain-dry-a")
	newReadyPodOn(t, c, "drain-dry", "drain-dry-b")

	exp := drainExperiment("drain-dry-run", "drain-dry")
	exp.Spec.Safety.DryRun = true
	if err := c.Create(context.Background(), exp); err != nil {
		t.Fatal(err)
	}
	eventually(t, 30*time.Second, "Completed phase", func() bool {
		return phaseOf(c, exp.Name) == tremorv1.PhaseCompleted
	})
	st := getExperiment(t, c, exp.Name).Status
	if !strings.Contains(st.Message, "draining node") || st.Node == "" {
		t.Errorf("dry run should name the node it would drain: %q (node %q)", st.Message, st.Node)
	}
	if st.RevertedAt != nil || st.InjectedAt != nil {
		t.Errorf("a dry run injects and releases nothing: %+v", st)
	}
	for _, n := range []string{"drain-dry-a", "drain-dry-b"} {
		if getNode(t, c, n).Spec.Unschedulable {
			t.Errorf("dry run cordoned %s", n)
		}
	}
}
