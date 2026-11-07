VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
COMMIT  ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo none)
IMG     ?= tremor-manager:$(VERSION)
LDFLAGS := -X github.com/Amitreddy14/tremor/internal/version.Version=$(VERSION) \
           -X github.com/Amitreddy14/tremor/internal/version.Commit=$(COMMIT)

.PHONY: help build test vet fmt fmt-check docker-build kind-up kind-up-lite kind-down

help: ## Show targets
	@grep -E '^[a-zA-Z_-]+:.*?## ' $(MAKEFILE_LIST) | awk 'BEGIN{FS=":.*?## "}{printf "  %-16s %s\n",$$1,$$2}'

build: ## Build the operator binary into ./bin
	go build -ldflags "$(LDFLAGS)" -o bin/manager ./cmd/manager

test: ## Run unit tests with the race detector
	go test -race ./...

vet: ## Run go vet
	go vet ./...

fmt: ## Format code
	gofmt -w .

fmt-check: ## Fail if code is not gofmt-clean
	@test -z "$$(gofmt -l .)" || (gofmt -l . && echo "run: make fmt" && exit 1)

docker-build: ## Build the operator image
	docker build --build-arg VERSION=$(VERSION) --build-arg COMMIT=$(COMMIT) -t $(IMG) .

kind-up: ## Create the 3-node kind cluster (1 control-plane, 2 workers)
	kind create cluster --name tremor --config hack/kind-config.yaml

kind-up-lite: ## Create the 2-node kind cluster for low-memory machines
	kind create cluster --name tremor --config hack/kind-config-lite.yaml

kind-down: ## Delete the kind cluster
	kind delete cluster --name tremor
