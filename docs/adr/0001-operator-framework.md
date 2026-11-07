# ADR 0001: Kubebuilder and controller-runtime for the operator

Status: accepted

## Context
Tremor must reconcile custom resources, manage finalizers, expose metrics and run leader election. The options were raw client-go, Operator SDK, or Kubebuilder.

## Decision
Use Kubebuilder conventions on top of controller-runtime. Generate CRDs and deepcopy code with controller-gen. Test controllers with envtest.

## Consequences
Standard layout that reviewers recognise; metrics, health probes and leader election come built in. Costs a generated-code step (`make generate`) and a dependency on the controller-runtime release cadence.
