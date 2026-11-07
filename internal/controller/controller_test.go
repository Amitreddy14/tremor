package controller

import (
	"context"
	"math/rand"
	"os"
	"path/filepath"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	tremorv1 "github.com/Amitreddy14/tremor/api/v1alpha1"
)

const testNamespace = "default"

// startEnv boots an API server with the CRD installed and the controller
// running. It skips when envtest binaries are unavailable (see `make test`).
func startEnv(t *testing.T) client.Client {
	t.Helper()
	if os.Getenv("KUBEBUILDER_ASSETS") == "" {
		t.Skip("KUBEBUILDER_ASSETS not set; run `make test` to include envtest")
	}

	env := &envtest.Environment{
		CRDDirectoryPaths:     []string{filepath.Join("..", "..", "config", "crd", "bases")},
		ErrorIfCRDPathMissing: true,
	}
	cfg, err := env.Start()
	if err != nil {
		t.Fatalf("starting envtest: %v", err)
	}
	t.Cleanup(func() { _ = env.Stop() })

	scheme := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := tremorv1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}

	mgr, err := ctrl.NewManager(cfg, ctrl.Options{
		Scheme:  scheme,
		Metrics: metricsserver.Options{BindAddress: "0"},
	})
	if err != nil {
		t.Fatalf("creating manager: %v", err)
	}
	r := &ChaosExperimentReconciler{
		Client: mgr.GetClient(),
		Scheme: scheme,
		Now:    time.Now,
		Rand:   rand.New(rand.NewSource(1)),
	}
	if err := r.SetupWithManager(mgr); err != nil {
		t.Fatalf("setting up controller: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go func() { _ = mgr.Start(ctx) }()

	c, err := client.New(cfg, client.Options{Scheme: scheme})
	if err != nil {
		t.Fatalf("creating client: %v", err)
	}
	return c
}

// newReadyPod creates a pod and marks it Ready, standing in for the kubelet,
// which envtest does not run.
func newReadyPod(t *testing.T, c client.Client, app string) *corev1.Pod {
	t.Helper()
	ctx := context.Background()
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			GenerateName: app + "-",
			Namespace:    testNamespace,
			Labels:       map[string]string{"app": app},
		},
		Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "c", Image: "busybox"}}},
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

func newExperiment(name, app string) *tremorv1.ChaosExperiment {
	return &tremorv1.ChaosExperiment{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: testNamespace},
		Spec: tremorv1.ChaosExperimentSpec{
			Target: tremorv1.TargetSpec{Selector: map[string]string{"app": app}},
			Fault:  tremorv1.FaultSpec{Type: tremorv1.FaultPodKill, Params: tremorv1.FaultParams{Count: 1}},
		},
	}
}

func eventually(t *testing.T, timeout time.Duration, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func phaseOf(c client.Client, name string) tremorv1.Phase {
	var exp tremorv1.ChaosExperiment
	if err := c.Get(context.Background(), types.NamespacedName{Namespace: testNamespace, Name: name}, &exp); err != nil {
		return ""
	}
	return exp.Status.Phase
}

func TestPodKillLifecycle(t *testing.T) {
	c := startEnv(t)
	ctx := context.Background()

	for i := 0; i < 3; i++ {
		newReadyPod(t, c, "victim")
	}

	exp := newExperiment("kill-victim", "victim")
	if err := c.Create(ctx, exp); err != nil {
		t.Fatal(err)
	}

	// The controller injects and then waits for recovery.
	eventually(t, 20*time.Second, "Recovering phase", func() bool {
		return phaseOf(c, "kill-victim") == tremorv1.PhaseRecovering
	})

	var list corev1.PodList
	if err := c.List(ctx, &list, client.InNamespace(testNamespace), client.MatchingLabels{"app": "victim"}); err != nil {
		t.Fatal(err)
	}
	if len(list.Items) != 2 {
		t.Fatalf("expected 2 pods after one kill, got %d", len(list.Items))
	}

	// Stand in for the owning ReplicaSet: create the replacement pod.
	newReadyPod(t, c, "victim")

	eventually(t, 20*time.Second, "Completed phase", func() bool {
		return phaseOf(c, "kill-victim") == tremorv1.PhaseCompleted
	})

	var done tremorv1.ChaosExperiment
	if err := c.Get(ctx, types.NamespacedName{Namespace: testNamespace, Name: "kill-victim"}, &done); err != nil {
		t.Fatal(err)
	}
	if done.Status.BaselineReadyPods != 3 {
		t.Errorf("baselineReadyPods = %d, want 3", done.Status.BaselineReadyPods)
	}
	if len(done.Status.AffectedPods) != 1 {
		t.Errorf("affectedPods = %d, want 1", len(done.Status.AffectedPods))
	}
	if done.Status.InjectedAt == nil || done.Status.RecoveredAt == nil {
		t.Fatalf("missing timestamps: %+v", done.Status)
	}
	if done.Status.PodRecoveryMillis == nil || *done.Status.PodRecoveryMillis < 0 {
		t.Errorf("podRecoveryMillis = %v, want >= 0", done.Status.PodRecoveryMillis)
	}
}

func TestSinglePodRefused(t *testing.T) {
	c := startEnv(t)
	ctx := context.Background()

	newReadyPod(t, c, "solo")
	if err := c.Create(ctx, newExperiment("kill-solo", "solo")); err != nil {
		t.Fatal(err)
	}

	eventually(t, 20*time.Second, "Failed phase", func() bool {
		return phaseOf(c, "kill-solo") == tremorv1.PhaseFailed
	})

	var list corev1.PodList
	if err := c.List(ctx, &list, client.InNamespace(testNamespace), client.MatchingLabels{"app": "solo"}); err != nil {
		t.Fatal(err)
	}
	if len(list.Items) != 1 {
		t.Fatalf("the only pod must survive, got %d pods", len(list.Items))
	}
}

func TestDryRunInjectsNothing(t *testing.T) {
	c := startEnv(t)
	ctx := context.Background()

	for i := 0; i < 3; i++ {
		newReadyPod(t, c, "dry")
	}
	exp := newExperiment("dry-run", "dry")
	exp.Spec.Safety.DryRun = true
	if err := c.Create(ctx, exp); err != nil {
		t.Fatal(err)
	}

	eventually(t, 20*time.Second, "Completed phase", func() bool {
		return phaseOf(c, "dry-run") == tremorv1.PhaseCompleted
	})

	var list corev1.PodList
	if err := c.List(ctx, &list, client.InNamespace(testNamespace), client.MatchingLabels{"app": "dry"}); err != nil {
		t.Fatal(err)
	}
	if len(list.Items) != 3 {
		t.Fatalf("dry run must not delete pods, got %d", len(list.Items))
	}
}
