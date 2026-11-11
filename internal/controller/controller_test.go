package controller

import (
	"context"
	"math/rand"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	tremorv1 "github.com/Amitreddy14/tremor/api/v1alpha1"
	"github.com/Amitreddy14/tremor/internal/fault"
	"github.com/Amitreddy14/tremor/internal/fault/stress"
	"github.com/Amitreddy14/tremor/internal/metrics"
	"github.com/Amitreddy14/tremor/internal/prober"
	"github.com/Amitreddy14/tremor/internal/runstore"
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
	// testHistory receives every finished run, like the MongoDB store would.
	testHistory = &runstore.MemoryStore{}
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
		// Same as cmd/manager: read workloads uncached so Revert sees Inject.
		Client: client.Options{Cache: &client.CacheOptions{
			DisableFor: []client.Object{&appsv1.Deployment{}, &appsv1.StatefulSet{}, &corev1.Node{}},
		}},
	})
	if err != nil {
		return nil, err
	}
	// A stress container that never starts must fail the run quickly in tests.
	fault.StartGrace = 3 * time.Second
	r := &ChaosExperimentReconciler{
		Client:          mgr.GetClient(),
		Scheme:          scheme,
		Now:             time.Now,
		Rand:            rand.New(rand.NewSource(1)),
		Prober:          prober.NewManager(),
		Metrics:         metrics.New(testReg),
		History:         testHistory,
		ClusterName:     "test",
		OperatorVersion: "vtest",
		StressImage:     "busybox:test",
		NetemImage:      "netem:test",
		// Probe tests serve from httptest on the loopback address.
		AllowedProbeHosts: []string{"127.0.0.1"},
	}
	if err := r.SetupWithManager(mgr); err != nil {
		return nil, err
	}
	sr := &ChaosScheduleReconciler{
		Client: mgr.GetClient(),
		Scheme: scheme,
		Now:    func() time.Time { return time.Now().Add(time.Duration(scheduleClockOffset.Load())) },
	}
	if err := sr.SetupWithManager(mgr); err != nil {
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
	return newReadyPodOwned(t, c, app, nil)
}

// newReadyPodOwned is newReadyPod with an optional controller owner reference.
func newReadyPodOwned(t *testing.T, c client.Client, app string, owner *metav1.OwnerReference) *corev1.Pod {
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
	if owner != nil {
		pod.OwnerReferences = []metav1.OwnerReference{*owner}
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

func TestFinishedRunIsRecordedOnce(t *testing.T) {
	c := startEnv(t)
	ctx := context.Background()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	for i := 0; i < 3; i++ {
		newReadyPod(t, c, "ledger")
	}
	exp := newExperiment("ledger-kill", "ledger")
	exp.Spec.SteadyState = steadyState(srv.URL)
	if err := c.Create(ctx, exp); err != nil {
		t.Fatal(err)
	}

	eventually(t, 30*time.Second, "Recovering phase", func() bool {
		return phaseOf(c, "ledger-kill") == tremorv1.PhaseRecovering
	})
	newReadyPod(t, c, "ledger")

	var uid string
	eventually(t, 30*time.Second, "run recorded flag", func() bool {
		e := getExperiment(t, c, "ledger-kill")
		uid = string(e.UID)
		return e.Status.Phase == tremorv1.PhaseCompleted && e.Status.RunRecorded
	})

	run, err := testHistory.Get(ctx, uid)
	if err != nil {
		t.Fatalf("run missing from history: %v", err)
	}
	if run.Result != "completed" || run.FaultType != "PodKill" || run.Namespace != testNamespace || run.Name != "ledger-kill" {
		t.Errorf("run identity wrong: %+v", run)
	}
	if run.Cluster != "test" || run.OperatorVersion != "vtest" {
		t.Errorf("cluster/version = %q/%q, want test/vtest", run.Cluster, run.OperatorVersion)
	}
	if run.BaselineReadyPods != 3 || len(run.AffectedPods) != 1 || run.Selector["app"] != "ledger" {
		t.Errorf("targeting wrong: %+v", run)
	}
	if run.PodRecoveryMillis == nil || run.UserRecoveryMillis == nil || run.SteadyStateURL != srv.URL {
		t.Errorf("recovery fields wrong: %+v", run)
	}
	if run.InjectedAt == nil || run.RecoveredAt == nil || run.StartedAt.IsZero() {
		t.Errorf("timestamps missing: %+v", run)
	}

	if got := metricSum("tremor_run_store_writes_total", map[string]string{"result": "ok"}); got < 1 {
		t.Errorf("run store write metric = %v, want >= 1", got)
	}
}

// ---- Phase 5: sustained faults --------------------------------------------

func int32Ptr(v int32) *int32 { return &v }

func boolPtr(v bool) *bool { return &v }

func podTemplate(app string) corev1.PodTemplateSpec {
	return corev1.PodTemplateSpec{
		ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{"app": app}},
		Spec:       corev1.PodSpec{Containers: []corev1.Container{{Name: "c", Image: "busybox"}}},
	}
}

// newWorkload creates a Deployment with its ReplicaSet and returns an owner
// reference for pods of that ReplicaSet. envtest runs no controllers, so the
// tests play the ReplicaSet controller themselves.
func newWorkload(t *testing.T, c client.Client, app string) *metav1.OwnerReference {
	t.Helper()
	ctx := context.Background()
	sel := &metav1.LabelSelector{MatchLabels: map[string]string{"app": app}}

	dep := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: app, Namespace: testNamespace},
		Spec:       appsv1.DeploymentSpec{Replicas: int32Ptr(1), Selector: sel, Template: podTemplate(app)},
	}
	if err := c.Create(ctx, dep); err != nil {
		t.Fatalf("creating deployment: %v", err)
	}
	rs := &appsv1.ReplicaSet{
		ObjectMeta: metav1.ObjectMeta{
			Name: app + "-rs", Namespace: testNamespace,
			OwnerReferences: []metav1.OwnerReference{{
				APIVersion: "apps/v1", Kind: "Deployment", Name: dep.Name, UID: dep.UID,
				Controller: boolPtr(true), BlockOwnerDeletion: boolPtr(true),
			}},
		},
		Spec: appsv1.ReplicaSetSpec{Replicas: int32Ptr(1), Selector: sel, Template: podTemplate(app)},
	}
	if err := c.Create(ctx, rs); err != nil {
		t.Fatalf("creating replicaset: %v", err)
	}
	return &metav1.OwnerReference{
		APIVersion: "apps/v1", Kind: "ReplicaSet", Name: rs.Name, UID: rs.UID,
		Controller: boolPtr(true), BlockOwnerDeletion: boolPtr(true),
	}
}

func getDeployment(t *testing.T, c client.Client, name string) appsv1.Deployment {
	t.Helper()
	var d appsv1.Deployment
	if err := c.Get(context.Background(), types.NamespacedName{Namespace: testNamespace, Name: name}, &d); err != nil {
		t.Fatal(err)
	}
	return d
}

func deploymentRestored(t *testing.T, c client.Client, name string) bool {
	d := getDeployment(t, c, name)
	_, held := d.Annotations[fault.AnnotationHeldBy]
	return d.Spec.Replicas != nil && *d.Spec.Replicas == 1 && !held
}

// deleteAllPods plays the ReplicaSet controller scaling down.
func deleteAllPods(t *testing.T, c client.Client, app string) {
	t.Helper()
	var list corev1.PodList
	if err := c.List(context.Background(), &list, client.InNamespace(testNamespace), client.MatchingLabels{"app": app}); err != nil {
		t.Fatal(err)
	}
	for i := range list.Items {
		if err := c.Delete(context.Background(), &list.Items[i], client.GracePeriodSeconds(0)); err != nil && !apierrors.IsNotFound(err) {
			t.Fatal(err)
		}
	}
}

func outageExperiment(name, app string, hold time.Duration) *tremorv1.ChaosExperiment {
	exp := newExperiment(name, app)
	exp.Spec.Fault = tremorv1.FaultSpec{
		Type:   tremorv1.FaultDependencyOutage,
		Params: tremorv1.FaultParams{Duration: &metav1.Duration{Duration: hold}},
	}
	exp.Spec.Safety.AllowTotalOutage = true
	return exp
}

func TestDependencyOutageLifecycle(t *testing.T) {
	c := startEnv(t)
	ctx := context.Background()

	owner := newWorkload(t, c, "outage-db")
	newReadyPodOwned(t, c, "outage-db", owner)

	exp := outageExperiment("db-outage", "outage-db", 2*time.Second)
	if err := c.Create(ctx, exp); err != nil {
		t.Fatal(err)
	}

	eventually(t, 30*time.Second, "Holding phase", func() bool {
		return phaseOf(c, "db-outage") == tremorv1.PhaseHolding
	})
	dep := getDeployment(t, c, "outage-db")
	if dep.Spec.Replicas == nil || *dep.Spec.Replicas != 0 {
		t.Fatalf("deployment replicas = %v, want 0 during the outage", dep.Spec.Replicas)
	}
	held := getExperiment(t, c, "db-outage")
	if dep.Annotations[fault.AnnotationHeldBy] != string(held.UID) || dep.Annotations[fault.AnnotationOriginalReplicas] != "1" {
		t.Errorf("deployment annotations = %v, want held-by the experiment and original replicas 1", dep.Annotations)
	}
	if w := held.Status.Workload; w == nil || w.Kind != "Deployment" || w.Name != "outage-db" {
		t.Errorf("status.workload = %+v, want Deployment/outage-db", w)
	}

	// Play the ReplicaSet controller: the scale-down removes the pod.
	deleteAllPods(t, c, "outage-db")

	eventually(t, 30*time.Second, "Recovering phase", func() bool {
		return phaseOf(c, "db-outage") == tremorv1.PhaseRecovering
	})
	if !deploymentRestored(t, c, "outage-db") {
		t.Fatalf("deployment not restored after the outage: %+v", getDeployment(t, c, "outage-db").Annotations)
	}

	// ... and scale back up.
	newReadyPodOwned(t, c, "outage-db", owner)
	eventually(t, 30*time.Second, "Completed phase", func() bool {
		return phaseOf(c, "db-outage") == tremorv1.PhaseCompleted
	})

	done := getExperiment(t, c, "db-outage")
	st := done.Status
	if st.InjectedAt == nil || st.RevertedAt == nil || st.PodsRecoveredAt == nil || st.PodRecoveryMillis == nil {
		t.Fatalf("missing timestamps or recovery: %+v", st)
	}
	if held := st.RevertedAt.Sub(st.InjectedAt.Time); held < 2*time.Second {
		t.Errorf("outage lasted %s, want at least the 2s duration", held)
	}
	if st.PodsRecoveredAt.Time.Before(st.RevertedAt.Time) {
		t.Errorf("pods recovered at %s, before the release at %s", st.PodsRecoveredAt, st.RevertedAt)
	}
	// Recovery is timed from the release: well under the 2s the outage lasted.
	if *st.PodRecoveryMillis < 0 || *st.PodRecoveryMillis >= 2000 {
		t.Errorf("podRecoveryMillis = %d, want measured from release (< 2000)", *st.PodRecoveryMillis)
	}

	eventually(t, 10*time.Second, "run in history", func() bool {
		r, err := testHistory.Get(ctx, string(done.UID))
		if err != nil {
			return false
		}
		d, ok := r.FaultActive()
		return ok && d >= 2*time.Second && r.FaultType == "DependencyOutage" && r.Result == "completed"
	})
}

func TestDependencyOutageNeedsConfirmation(t *testing.T) {
	c := startEnv(t)
	exp := outageExperiment("db-unconfirmed", "unconfirmed-db", 2*time.Second)
	exp.Spec.Safety.AllowTotalOutage = false
	if err := c.Create(context.Background(), exp); err != nil {
		t.Fatal(err)
	}
	eventually(t, 20*time.Second, "Failed phase", func() bool {
		return phaseOf(c, "db-unconfirmed") == tremorv1.PhaseFailed
	})
	if msg := getExperiment(t, c, "db-unconfirmed").Status.Message; !strings.Contains(msg, "allowTotalOutage") {
		t.Errorf("message = %q, want it to mention allowTotalOutage", msg)
	}
}

func TestAbortWhileHoldingRestoresWorkload(t *testing.T) {
	c := startEnv(t)
	ctx := context.Background()

	owner := newWorkload(t, c, "abort-db")
	newReadyPodOwned(t, c, "abort-db", owner)
	if err := c.Create(ctx, outageExperiment("abort-outage", "abort-db", 5*time.Minute)); err != nil {
		t.Fatal(err)
	}
	eventually(t, 30*time.Second, "Holding phase", func() bool {
		return phaseOf(c, "abort-outage") == tremorv1.PhaseHolding
	})
	if d := getDeployment(t, c, "abort-db"); d.Spec.Replicas == nil || *d.Spec.Replicas != 0 {
		t.Fatalf("replicas = %v, want 0 while held", d.Spec.Replicas)
	}

	exp := getExperiment(t, c, "abort-outage")
	patch := client.MergeFrom(exp.DeepCopy())
	exp.Annotations = map[string]string{tremorv1.AbortAnnotation: "true"}
	if err := c.Patch(ctx, &exp, patch); err != nil {
		t.Fatal(err)
	}

	eventually(t, 30*time.Second, "Aborted phase", func() bool {
		return phaseOf(c, "abort-outage") == tremorv1.PhaseAborted
	})
	if !deploymentRestored(t, c, "abort-db") {
		t.Fatalf("abort left the dependency down: replicas=%v annotations=%v",
			getDeployment(t, c, "abort-db").Spec.Replicas, getDeployment(t, c, "abort-db").Annotations)
	}
	if getExperiment(t, c, "abort-outage").Status.RevertedAt == nil {
		t.Error("revertedAt not recorded for the aborted run")
	}
}

func TestDeletingExperimentWhileHoldingRestoresWorkload(t *testing.T) {
	c := startEnv(t)
	ctx := context.Background()

	owner := newWorkload(t, c, "delete-db")
	newReadyPodOwned(t, c, "delete-db", owner)
	if err := c.Create(ctx, outageExperiment("delete-outage", "delete-db", 5*time.Minute)); err != nil {
		t.Fatal(err)
	}
	eventually(t, 30*time.Second, "Holding phase", func() bool {
		return phaseOf(c, "delete-outage") == tremorv1.PhaseHolding
	})

	exp := getExperiment(t, c, "delete-outage")
	if err := c.Delete(ctx, &exp); err != nil {
		t.Fatal(err)
	}
	eventually(t, 30*time.Second, "experiment gone and workload restored", func() bool {
		var e tremorv1.ChaosExperiment
		err := c.Get(ctx, types.NamespacedName{Namespace: testNamespace, Name: "delete-outage"}, &e)
		return apierrors.IsNotFound(err) && deploymentRestored(t, c, "delete-db")
	})
}

func TestStressFaults(t *testing.T) {
	tests := []struct {
		name   string
		app    string
		fault  tremorv1.FaultType
		params tremorv1.FaultParams
		script string // must appear in the container command
	}{
		{
			"CPUStress", "burn-cpu", tremorv1.FaultCPUStress,
			tremorv1.FaultParams{Count: 1, CPUWorkers: 2, Duration: &metav1.Duration{Duration: 2 * time.Second}},
			"-lt 2",
		},
		{
			"MemoryStress", "burn-mem", tremorv1.FaultMemoryStress,
			tremorv1.FaultParams{Count: 1, MemoryMiB: 64, Duration: &metav1.Duration{Duration: 2 * time.Second}},
			"bs=67108864",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := startEnv(t)
			ctx := context.Background()
			for i := 0; i < 3; i++ {
				newReadyPod(t, c, tt.app)
			}
			exp := newExperiment(tt.app+"-run", tt.app)
			exp.Spec.Fault = tremorv1.FaultSpec{Type: tt.fault, Params: tt.params}
			if err := c.Create(ctx, exp); err != nil {
				t.Fatal(err)
			}

			eventually(t, 30*time.Second, "Holding phase", func() bool {
				return phaseOf(c, exp.Name) == tremorv1.PhaseHolding
			})
			run := getExperiment(t, c, exp.Name)
			if len(run.Status.AffectedPods) != 1 {
				t.Fatalf("affected pods = %d, want 1", len(run.Status.AffectedPods))
			}

			var pod corev1.Pod
			key := types.NamespacedName{Namespace: testNamespace, Name: run.Status.AffectedPods[0].Name}
			if err := c.Get(ctx, key, &pod); err != nil {
				t.Fatal(err)
			}
			name := stress.ContainerName(string(run.UID))
			if len(pod.Spec.EphemeralContainers) != 1 || pod.Spec.EphemeralContainers[0].Name != name {
				t.Fatalf("ephemeral containers = %+v, want one named %s", pod.Spec.EphemeralContainers, name)
			}
			ec := pod.Spec.EphemeralContainers[0]
			if ec.Image != "busybox:test" || len(ec.Command) != 3 || !strings.Contains(ec.Command[2], tt.script) {
				t.Errorf("container image/command wrong: %s %v", ec.Image, ec.Command)
			}
			if ec.SecurityContext == nil || ec.SecurityContext.RunAsNonRoot == nil || !*ec.SecurityContext.RunAsNonRoot {
				t.Errorf("stress container should run as non-root: %+v", ec.SecurityContext)
			}

			// Play the kubelet: the container is running. Past its 2s duration the
			// experiment must still wait for the container, not a timer.
			pod.Status.EphemeralContainerStatuses = []corev1.ContainerStatus{{
				Name: name, State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{}},
			}}
			if err := c.Status().Update(ctx, &pod); err != nil {
				t.Fatal(err)
			}
			time.Sleep(3 * time.Second)
			if p := phaseOf(c, exp.Name); p != tremorv1.PhaseHolding {
				t.Fatalf("phase = %s while the stress container still runs, want Holding", p)
			}

			// The container finishes.
			if err := c.Get(ctx, key, &pod); err != nil {
				t.Fatal(err)
			}
			pod.Status.EphemeralContainerStatuses = []corev1.ContainerStatus{{
				Name: name, State: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{ExitCode: 0}},
			}}
			if err := c.Status().Update(ctx, &pod); err != nil {
				t.Fatal(err)
			}
			eventually(t, 30*time.Second, "Completed phase", func() bool {
				return phaseOf(c, exp.Name) == tremorv1.PhaseCompleted
			})

			done := getExperiment(t, c, exp.Name)
			if done.Status.RevertedAt == nil || done.Status.PodRecoveryMillis == nil {
				t.Fatalf("missing release or recovery: %+v", done.Status)
			}
			var list corev1.PodList
			if err := c.List(ctx, &list, client.InNamespace(testNamespace), client.MatchingLabels{"app": tt.app}); err != nil {
				t.Fatal(err)
			}
			if len(list.Items) != 3 {
				t.Errorf("stress must not remove pods: %d left, want 3", len(list.Items))
			}
		})
	}
}

func TestStressThatNeverStartsFails(t *testing.T) {
	c := startEnv(t)
	for i := 0; i < 3; i++ {
		newReadyPod(t, c, "burn-stuck")
	}
	exp := newExperiment("stuck-stress", "burn-stuck")
	exp.Spec.Fault = tremorv1.FaultSpec{
		Type:   tremorv1.FaultCPUStress,
		Params: tremorv1.FaultParams{Count: 1, Duration: &metav1.Duration{Duration: time.Second}},
	}
	if err := c.Create(context.Background(), exp); err != nil {
		t.Fatal(err)
	}
	// Nothing plays the kubelet, so the container never reports a status.
	eventually(t, 30*time.Second, "Failed phase", func() bool {
		return phaseOf(c, "stuck-stress") == tremorv1.PhaseFailed
	})
	if msg := getExperiment(t, c, "stuck-stress").Status.Message; !strings.Contains(msg, "did not start") {
		t.Errorf("message = %q, want it to say the stress container did not start", msg)
	}
}

// An outage users feel must report user recovery from the moment the fault was
// released, not from its start: the 3s hold is not recovery time.
func TestOutageUserRecoveryIsMeasuredFromRelease(t *testing.T) {
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

	owner := newWorkload(t, c, "felt-db")
	newReadyPodOwned(t, c, "felt-db", owner)
	exp := outageExperiment("felt-outage", "felt-db", 3*time.Second)
	exp.Spec.SteadyState = steadyState(srv.URL)
	if err := c.Create(ctx, exp); err != nil {
		t.Fatal(err)
	}

	eventually(t, 30*time.Second, "Holding phase", func() bool {
		return phaseOf(c, "felt-outage") == tremorv1.PhaseHolding
	})
	failing.Store(true) // users see errors while the dependency is down
	deleteAllPods(t, c, "felt-db")

	eventually(t, 30*time.Second, "Recovering phase", func() bool {
		return phaseOf(c, "felt-outage") == tremorv1.PhaseRecovering
	})
	failing.Store(false) // the service answers again right after the release
	newReadyPodOwned(t, c, "felt-db", owner)

	eventually(t, 30*time.Second, "Completed phase", func() bool {
		return phaseOf(c, "felt-outage") == tremorv1.PhaseCompleted
	})

	st := getExperiment(t, c, "felt-outage").Status
	if !st.UserImpacted || st.ProbeFailures == 0 {
		t.Fatalf("outage should be user-visible: impacted=%v failures=%d", st.UserImpacted, st.ProbeFailures)
	}
	if st.UserRecoveryMillis == nil {
		t.Fatal("userRecoveryMillis not set")
	}
	// Measured from injection it would be at least the 3s hold.
	if *st.UserRecoveryMillis < 0 || *st.UserRecoveryMillis >= 3000 {
		t.Errorf("userRecoveryMillis = %d, want measured from release (0 to 3000)", *st.UserRecoveryMillis)
	}
}
