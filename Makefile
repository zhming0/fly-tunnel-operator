IMG ?= fly-tunnel-operator:latest

.PHONY: all
all: build

## Build
.PHONY: build
build:
	go build -o bin/manager .

.PHONY: run
run:
	go run .

.PHONY: test
test:
	go test ./... -v

.PHONY: lint
lint:
	golangci-lint run

.PHONY: fmt
fmt:
	go fmt ./...

.PHONY: vet
vet:
	go vet ./...

## Docker
.PHONY: docker-build
docker-build:
	docker build -t $(IMG) .

.PHONY: docker-push
docker-push:
	docker push $(IMG)

## Install/Deploy (using Helm)
.PHONY: helm-install
helm-install:
	helm install fly-tunnel-operator charts/fly-tunnel-operator

.PHONY: helm-uninstall
helm-uninstall:
	helm uninstall fly-tunnel-operator

.PHONY: helm-template
helm-template:
	helm template fly-tunnel-operator charts/fly-tunnel-operator

## Manifests
.PHONY: manifests
manifests:
	@echo "No CRDs to generate — this operator uses only core Kubernetes types."

.PHONY: clean
clean:
	rm -rf bin/
