package controller

import (
	"context"
	"math/rand"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
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
	"github.com/Amitreddy14/tremor/internal/metrics"
	"github.com/Amitreddy14/tremor/internal/prober"
	"github.com/prometheus/client_golang/prometheus"
)

const testNamespace = "default"

var (
	envOnce   sync.Once
	envClient client.Client
	envErr    error
	testEnv   *envtest.Environment
	stopMgr   context.CancelFunc
	testReg   = prometheus.NewRegistry()
)

// TestMain stops the shared test environment after all tests have run.
func TestMain(m *testing.M) {
	code := m.Run()
	if stopMgr != nil {
		stopMgr()
	}
	if testEnv != nil {
		_ = testEnv.Stop()
	}
	os.Exit(code)
}

// startEnv returns a client for a shared API server with the CRD installed and
// the controller running. One environment serves every test because
// controller-runtime allows each controller name only once per process. It
// skips when envtest binaries are unavailable (see `make test`).
func startEnv(t *testing.T) client.Client {
	t.Helper()
	if os.Getenv("KUBEBUILDER_ASSETS") == "" {
		t.Skip("KUBEBUILDER_ASSETS not set; run `make test` to include envtest")
	}
	envOnce.Do(func() { envClient, envErr = bootEnv() })
	if envErr != nil {
		t.Fatalf("starting test environment: %v", envErr)
	}
	return envClient
}

func bootEnv() (client.Client, error) {
	testEnv = &envtest.Environment{
		CRDDirectoryPaths:     []string{filepath.Join("..", "..", "config", "crd", "bases")},
		ErrorIfCRDPathMissing: true,
	}
	cfg, err := testEnv.Start()
	if err != nil {
		return nil, err
	}

	scheme := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(scheme); err != nil {
		return nil, err
	}
	if err := tremorv1.AddToScheme(scheme); err != nil {
		return nil, err
	}

	mgr, err := ctrl.NewManager(cfg, ctrl.Options{
		Scheme:  scheme,
		Metrics: metricsserver.Options{BindAddress: "0"},
	})
	if err != nil {
		return nil, err
	}
	r := &ChaosExperimentReconciler{
		Client:  mgr.GetClient(),
		Scheme:  scheme,
		Now:     time.Now,
		Rand:    rand.New(rand.NewSource(1)),
		Prober:  prober.NewManager(),
		Metrics: metrics.New(testReg),
	}
	if err := r.SetupWithManager(mgr); err != nil {
		return nil, err
	}

	ctx, cancel := context.WithCancel(context.Background())
	stopMgr = cancel
	go func() { _ = mgr.Start(ctx) }()

	return client.New(cfg, client.Options{Scheme: scheme})
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

func steadyState(url string) *tremorv1.SteadyStateSpec {
	return &tremorv1.SteadyStateSpec{
		HTTP:             tremorv1.HTTPProbeSpec{URL: url, ExpectStatus: 200},
		Interval:         &metav1.Duration{Duration: 100 * time.Millisecond},
		SuccessThreshold: 3,
	}
}

func getExperiment(t *testing.T, c client.Client, name string) tremorv1.ChaosExperiment {
	t.Helper()
	var exp tremorv1.ChaosExperiment
	if err := c.Get(context.Background(), types.NamespacedName{Namespace: testNamespace, Name: name}, &exp); err != nil {
		t.Fatal(err)
	}
	return exp
}

func TestSteadyStateNoUserImpact(t *testing.T) {
	c := startEnv(t)
	ctx := context.Background()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	for i := 0; i < 3; i++ {
		newReadyPod(t, c, "calm")
	}
	exp := newExperiment("calm-kill", "calm")
	exp.Spec.SteadyState = steadyState(srv.URL)
	if err := c.Create(ctx, exp); err != nil {
		t.Fatal(err)
	}

	eventually(t, 30*time.Second, "Recovering phase", func() bool {
		return phaseOf(c, "calm-kill") == tremorv1.PhaseRecovering
	})
	newReadyPod(t, c, "calm")
	eventually(t, 30*time.Second, "Completed phase", func() bool {
		return phaseOf(c, "calm-kill") == tremorv1.PhaseCompleted
	})

	done := getExperiment(t, c, "calm-kill")
	if done.Status.UserImpacted {
		t.Error("userImpacted = true, want false")
	}
	if done.Status.UserRecoveryMillis == nil || *done.Status.UserRecoveryMillis != 0 {
		t.Errorf("userRecoveryMillis = %v, want 0", done.Status.UserRecoveryMillis)
	}
	if done.Status.ProbeSuccesses < 3 || done.Status.ProbeFailures != 0 {
		t.Errorf("probes = %d ok / %d failed, want >=3 ok and 0 failed", done.Status.ProbeSuccesses, done.Status.ProbeFailures)
	}
}

func TestSteadyStateMeasuresUserImpact(t *testing.T) {
	c := startEnv(t)
	ctx := context.Background()

	var failing atomic.Bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if failing.Load() {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	for i := 0; i < 3; i++ {
		newReadyPod(t, c, "hurt")
	}
	exp := newExperiment("hurt-kill", "hurt")
	exp.Spec.SteadyState = steadyState(srv.URL)
	if err := c.Create(ctx, exp); err != nil {
		t.Fatal(err)
	}

	eventually(t, 30*time.Second, "Recovering phase", func() bool {
		return phaseOf(c, "hurt-kill") == tremorv1.PhaseRecovering
	})

	// Simulate users seeing errors for a while after the pod kill.
	failing.Store(true)
	time.Sleep(800 * time.Millisecond)
	failing.Store(false)
	newReadyPod(t, c, "hurt")

	eventually(t, 30*time.Second, "Completed phase", func() bool {
		return phaseOf(c, "hurt-kill") == tremorv1.PhaseCompleted
	})

	done := getExperiment(t, c, "hurt-kill")
	if !done.Status.UserImpacted {
		t.Error("userImpacted = false, want true")
	}
	if done.Status.UserRecoveryMillis == nil || *done.Status.UserRecoveryMillis < 600 {
		t.Errorf("userRecoveryMillis = %v, want >= 600", done.Status.UserRecoveryMillis)
	}
	if done.Status.ProbeFailures == 0 {
		t.Error("probeFailures = 0, want > 0")
	}
}

// metricSum adds up every sample of a metric family whose labels contain want.
// Histograms contribute their observation count.
func metricSum(name string, want map[string]string) float64 {
	families, err := testReg.Gather()
	if err != nil {
		return 0
	}
	var total float64
	for _, f := range families {
		if f.GetName() != name {
			continue
		}
		for _, m := range f.GetMetric() {
			labels := map[string]string{}
			for _, l := range m.GetLabel() {
				labels[l.GetName()] = l.GetValue()
			}
			match := true
			for k, v := range want {
				if labels[k] != v {
					match = false
				}
			}
			if !match {
				continue
			}
			switch {
			case m.GetCounter() != nil:
				total += m.GetCounter().GetValue()
			case m.GetHistogram() != nil:
				total += float64(m.GetHistogram().GetSampleCount())
			case m.GetGauge() != nil:
				total += m.GetGauge().GetValue()
			}
		}
	}
	return total
}

func TestMetricsAreRecorded(t *testing.T) {
	c := startEnv(t)
	ctx := context.Background()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	for i := 0; i < 3; i++ {
		newReadyPod(t, c, "metered")
	}
	exp := newExperiment("metered-kill", "metered")
	exp.Spec.SteadyState = steadyState(srv.URL)
	if err := c.Create(ctx, exp); err != nil {
		t.Fatal(err)
	}
	eventually(t, 30*time.Second, "Recovering phase", func() bool {
		return phaseOf(c, "metered-kill") == tremorv1.PhaseRecovering
	})
	newReadyPod(t, c, "metered")
	eventually(t, 30*time.Second, "Completed phase", func() bool {
		return phaseOf(c, "metered-kill") == tremorv1.PhaseCompleted
	})

	// Metrics are recorded just after the status update, so poll briefly.
	eventually(t, 10*time.Second, "completed experiment counter", func() bool {
		return metricSum("tremor_experiments_total", map[string]string{"type": "PodKill", "result": "completed"}) >= 1
	})
	if got := metricSum("tremor_faults_injected_total", map[string]string{"type": "PodKill"}); got < 1 {
		t.Errorf("faults injected = %v, want >= 1", got)
	}
	for _, kind := range []string{"pod", "user"} {
		if got := metricSum("tremor_recovery_seconds", map[string]string{"kind": kind}); got < 1 {
			t.Errorf("recovery histogram kind=%s has %v observations, want >= 1", kind, got)
		}
	}
	if got := metricSum("tremor_last_availability_ratio", map[string]string{"experiment": "metered-kill"}); got <= 0 {
		t.Errorf("availability = %v, want > 0", got)
	}
	if got := metricSum("tremor_experiment_phase", map[string]string{"experiment": "metered-kill", "phase": "Completed"}); got != 1 {
		t.Errorf("Completed phase gauge = %v, want 1", got)
	}
}
