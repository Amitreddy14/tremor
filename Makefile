VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
COMMIT  ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo none)
IMG     ?= tremor-manager:dev
IMG_SHOP ?= tremor-shop:dev
IMG_STRESS ?= busybox:1.36
IMG_NETEM ?= tremor-netem:dev
LDFLAGS := -X github.com/Amitreddy14/tremor/internal/version.Version=$(VERSION) \
           -X github.com/Amitreddy14/tremor/internal/version.Commit=$(COMMIT)

LOCALBIN       ?= $(CURDIR)/bin
CONTROLLER_GEN ?= $(LOCALBIN)/controller-gen
SETUP_ENVTEST  ?= $(LOCALBIN)/setup-envtest
ENVTEST_K8S    ?= 1.31.0
KIND_CLUSTER   ?= tremor

.PHONY: help build test test-unit vet fmt fmt-check generate manifests docker-build \
        docker-build-shop docker-build-netem netem-load demo-deploy demo-undeploy e2e-faults e2e-schedule \
        monitoring-up monitoring-apply monitoring-down grafana prometheus kind-up kind-up-lite kind-down kind-deploy history-forward tremorctl e2e undeploy

help: ## Show targets
	@grep -E '^[a-zA-Z_-]+:.*?## ' $(MAKEFILE_LIST) | awk 'BEGIN{FS=":.*?## "}{printf "  %-14s %s\n",$$1,$$2}'

build: ## Build the operator and tremorctl binaries into ./bin
	go build -ldflags "$(LDFLAGS)" -o bin/manager ./cmd/manager
	go build -ldflags "$(LDFLAGS)" -o bin/tremorctl ./cmd/tremorctl

$(CONTROLLER_GEN):
	GOBIN=$(LOCALBIN) go install sigs.k8s.io/controller-tools/cmd/controller-gen@v0.16.5

$(SETUP_ENVTEST):
	GOBIN=$(LOCALBIN) go install sigs.k8s.io/controller-runtime/tools/setup-envtest@release-0.19

generate: $(CONTROLLER_GEN) ## Regenerate deepcopy code
	$(CONTROLLER_GEN) object paths="./api/..."

manifests: $(CONTROLLER_GEN) ## Regenerate CRD and RBAC from markers
	$(CONTROLLER_GEN) crd rbac:roleName=tremor-manager-role paths="./api/..." paths="./internal/..." \
		output:crd:artifacts:config=config/crd/bases output:rbac:artifacts:config=config/rbac

test-unit: ## Unit tests only (no envtest needed; envtest tests skip)
	go test -race ./...

test: $(SETUP_ENVTEST) ## Unit tests plus envtest controller tests
	KUBEBUILDER_ASSETS="$$($(SETUP_ENVTEST) use $(ENVTEST_K8S) --bin-dir $(LOCALBIN) -p path)" go test -race ./...

vet: ## Run go vet
	go vet ./...

fmt: ## Format code
	gofmt -w .

fmt-check: ## Fail if code is not gofmt-clean
	@test -z "$$(gofmt -l .)" || (gofmt -l . && echo "run: make fmt" && exit 1)

docker-build: ## Build the operator image
	docker build --build-arg TARGET=manager --build-arg VERSION=$(VERSION) --build-arg COMMIT=$(COMMIT) -t $(IMG) .

docker-build-shop: ## Build the demo shop image
	docker build --build-arg TARGET=shop --build-arg VERSION=$(VERSION) --build-arg COMMIT=$(COMMIT) -t $(IMG_SHOP) .

docker-build-netem: ## Build the image that runs network faults (tc/netem)
	docker build -f Dockerfile.netem -t $(IMG_NETEM) .

netem-load: docker-build-netem ## Build the netem image and load it into kind
	kind load docker-image $(IMG_NETEM) --name $(KIND_CLUSTER)

demo-deploy: docker-build-shop ## Build the shop, load into kind, deploy shop + MongoDB
	kind load docker-image $(IMG_SHOP) --name $(KIND_CLUSTER)
	kubectl apply -k config/demo
	kubectl -n demo rollout status deploy/mongo --timeout=180s
	kubectl -n demo rollout status deploy/shop --timeout=180s

demo-undeploy: ## Remove the demo workload
	kubectl delete -k config/demo --ignore-not-found

kind-up: ## Create the 3-node kind cluster (1 control-plane, 2 workers)
	kind create cluster --name $(KIND_CLUSTER) --config hack/kind-config.yaml

kind-up-lite: ## Create the 2-node kind cluster for low-memory machines
	kind create cluster --name $(KIND_CLUSTER) --config hack/kind-config-lite.yaml

kind-down: ## Delete the kind cluster
	kind delete cluster --name $(KIND_CLUSTER)

kind-deploy: docker-build ## Build, load into kind, deploy the operator and its history MongoDB
	kind load docker-image $(IMG) --name $(KIND_CLUSTER)
	docker pull $(IMG_STRESS)
	kind load docker-image $(IMG_STRESS) --name $(KIND_CLUSTER)
	kubectl apply -k config/history
	kubectl -n tremor-system rollout status deploy/tremor-history --timeout=180s
	kubectl -n tremor-system rollout status deploy/tremor-manager --timeout=120s

history-forward: ## Port-forward the history MongoDB to localhost:27017
	kubectl -n tremor-system port-forward svc/tremor-history 27017:27017

tremorctl: ## Build the tremorctl CLI into ./bin
	go build -ldflags "$(LDFLAGS)" -o bin/tremorctl ./cmd/tremorctl

e2e: ## Run the end-to-end script (run demo-deploy first)
	bash ./hack/e2e.sh

e2e-faults: ## Network and node fault checks (run demo-deploy, kind-deploy and netem-load first)
	bash ./hack/e2e-faults.sh

e2e-schedule: ## Safety guards and schedule checks (run demo-deploy and kind-deploy first)
	bash ./hack/e2e-schedule.sh

monitoring-up: ## Install kube-prometheus-stack (Prometheus + Grafana) into kind via Helm
	helm repo add prometheus-community https://prometheus-community.github.io/helm-charts --force-update
	helm upgrade --install kps prometheus-community/kube-prometheus-stack \
		--namespace monitoring --create-namespace -f hack/kps-values.yaml --wait --timeout 10m

monitoring-apply: ## Add the Tremor ServiceMonitor and Grafana dashboard
	kubectl apply -k config/monitoring

grafana: ## Open Grafana on http://localhost:3000 (admin / tremor)
	kubectl -n monitoring port-forward svc/kps-grafana 3000:80

prometheus: ## Open Prometheus on http://localhost:9090
	kubectl -n monitoring port-forward svc/kps-prometheus 9090:9090

monitoring-down: ## Remove the monitoring stack and Tremor dashboard
	kubectl delete -k config/monitoring --ignore-not-found
	helm uninstall kps --namespace monitoring --ignore-not-found || true

undeploy: ## Remove the operator and CRD
	kubectl delete -k config/default --ignore-not-found
