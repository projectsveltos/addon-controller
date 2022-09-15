
# Image URL to use all building/pushing image targets
IMG ?= controller:latest
# KUBEBUILDER_ENVTEST_KUBERNETES_VERSION refers to the version of kubebuilder assets to be downloaded by envtest binary.
KUBEBUILDER_ENVTEST_KUBERNETES_VERSION = 1.24.1

# Get the currently used golang install path (in GOPATH/bin, unless GOBIN is set)
ifeq (,$(shell go env GOBIN))
GOBIN=$(shell go env GOPATH)/bin
else
GOBIN=$(shell go env GOBIN)
endif
GO_INSTALL := ./scripts/go_install.sh

# Setting SHELL to bash allows bash commands to be executed by recipes.
# This is a requirement for 'setup-envtest.sh' in the test target.
# Options are set to exit when a recipe line exits non-zero or a piped command fails.
SHELL = /usr/bin/env bash -o pipefail
.SHELLFLAGS = -ec

# Define Docker related variables.
REGISTRY ?= projectsveltos
IMAGE_NAME ?= sveltos-manager
ARCH ?= amd64
OS ?= $(shell uname -s | tr A-Z a-z)
K8S_LATEST_VER ?= $(shell curl -s https://storage.googleapis.com/kubernetes-release/release/stable.txt)
export CONTROLLER_IMG ?= $(REGISTRY)/$(IMAGE_NAME)
TAG ?= dev

# Get cluster-api version and build ldflags
clusterapi := $(shell go list -m sigs.k8s.io/cluster-api)
clusterapi_version := $(lastword  ., ,$(clusterapi))
clusterapi_version_tuple := $(subst ., ,$(clusterapi_version:v%=%))
clusterapi_major := $(word 1,$(clusterapi_version_tuple))
clusterapi_minor := $(word 2,$(clusterapi_version_tuple))
clusterapi_patch := $(word 3,$(clusterapi_version_tuple))
CLUSTERAPI_LDFLAGS := "-X 'sigs.k8s.io/cluster-api/version.gitMajor=$(clusterapi_major)' -X 'sigs.k8s.io/cluster-api/version.gitMinor=$(clusterapi_minor)' -X 'sigs.k8s.io/cluster-api/version.gitVersion=$(clusterapi_version)'"

.PHONY: all
all: build

##@ General

# The help target prints out all targets with their descriptions organized
# beneath their categories. The categories are represented by '##@' and the
# target descriptions by '##'. The awk commands is responsible for reading the
# entire set of makefiles included in this invocation, looking for lines of the
# file as xyz: ## something, and then pretty-format the target and help. Then,
# if there's a line with ##@ something, that gets pretty-printed as a category.
# More info on the usage of ANSI control characters for terminal formatting:
# https://en.wikipedia.org/wiki/ANSI_escape_code#SGR_parameters
# More info on the awk command:
# http://linuxcommand.org/lc3_adv_awk.php

.PHONY: help
help: ## Display this help.
	@awk 'BEGIN {FS = ":.*##"; printf "\nUsage:\n  make \033[36m<target>\033[0m\n"} /^[a-zA-Z_0-9-]+:.*?##/ { printf "  \033[36m%-15s\033[0m %s\n", $$1, $$2 } /^##@/ { printf "\n\033[1m%s\033[0m\n", substr($$0, 5) } ' $(MAKEFILE_LIST)

# Directories.
ROOT_DIR:=$(shell dirname $(realpath $(firstword $(MAKEFILE_LIST))))
TOOLS_DIR := hack/tools
TOOLS_BIN_DIR := $(TOOLS_DIR)/bin
BIN_DIR := bin

GOBUILD=go build

## Tool Binaries
CONTROLLER_GEN := $(TOOLS_BIN_DIR)/controller-gen
ENVSUBST := $(TOOLS_BIN_DIR)/envsubst
GOIMPORTS := $(TOOLS_BIN_DIR)/goimports
GOLANGCI_LINT := $(TOOLS_BIN_DIR)/golangci-lint
KUSTOMIZE := $(TOOLS_BIN_DIR)/kustomize
GINKGO := $(TOOLS_BIN_DIR)/ginkgo
SETUP_ENVTEST := $(TOOLS_BIN_DIR)/setup_envs
CLUSTERCTL := $(TOOLS_BIN_DIR)/clusterctl
KIND := $(TOOLS_BIN_DIR)/kind
KUBECTL := $(TOOLS_BIN_DIR)/kubectl


$(CONTROLLER_GEN): $(TOOLS_DIR)/go.mod # Build controller-gen from tools folder.
	cd $(TOOLS_DIR); $(GOBUILD) -tags=tools -o $(subst hack/tools/,,$@) sigs.k8s.io/controller-tools/cmd/controller-gen

$(ENVSUBST): $(TOOLS_DIR)/go.mod # Build envsubst from tools folder.
	cd $(TOOLS_DIR); $(GOBUILD) -tags=tools -o $(subst hack/tools/,,$@) github.com/a8m/envsubst/cmd/envsubst

$(KUSTOMIZE): $(TOOLS_DIR)/go.mod # Build kustomize from tools folder.
	cd $(TOOLS_DIR); $(GOBUILD) -tags=tools -o $(BIN_DIR)/kustomize sigs.k8s.io/kustomize/kustomize/v4

$(GOLANGCI_LINT): $(TOOLS_DIR)/go.mod # Build golangci-lint from tools folder.
	cd $(TOOLS_DIR); $(GOBUILD) -tags=tools -o $(subst hack/tools/,,$@) github.com/golangci/golangci-lint/cmd/golangci-lint

$(SETUP_ENVTEST): $(TOOLS_DIR)/go.mod # Build setup-envtest from tools folder.
	cd $(TOOLS_DIR); $(GOBUILD) -tags=tools -o $(subst hack/tools/,,$@) sigs.k8s.io/controller-runtime/tools/setup-envtest

$(GOIMPORTS):
	cd $(TOOLS_DIR); $(GOBUILD) -tags=tools -o $(subst hack/tools/,,$@) golang.org/x/tools/cmd/goimports

$(GINKGO): $(TOOLS_DIR)/go.mod
	cd $(TOOLS_DIR) && $(GOBUILD) -tags tools -o $(subst hack/tools/,,$@) github.com/onsi/ginkgo/v2/ginkgo

$(CLUSTERCTL): $(TOOLS_DIR)/go.mod ## Build clusterctl binary
	cd $(TOOLS_DIR); $(GOBUILD) -ldflags $(CLUSTERAPI_LDFLAGS) -o $(subst hack/tools/,,$@) sigs.k8s.io/cluster-api/cmd/clusterctl
	mkdir -p $(HOME)/.cluster-api # create cluster api init directory, if not present

$(KIND): $(TOOLS_DIR)/go.mod
	cd $(TOOLS_DIR) && $(GOBUILD) -tags tools -o $(subst hack/tools/,,$@) sigs.k8s.io/kind

$(KUBECTL):
	curl -L https://storage.googleapis.com/kubernetes-release/release/$(K8S_LATEST_VER)/bin/$(OS)/$(ARCH)/kubectl -o $@
	chmod +x $@

.PHONY: tools
tools: $(CONTROLLER_GEN) $(ENVSUBST) $(KUSTOMIZE) $(GOLANGCI_LINT) $(SETUP_ENVTEST) $(GOIMPORTS) $(GINKGO) \
	$(CLUSTERCTL) $(KIND) $(KUBECTL) ## build all tools

##@ Development

.PHONY: manifests
manifests: $(CONTROLLER_GEN) ## Generate WebhookConfiguration, ClusterRole and CustomResourceDefinition objects.
	$(CONTROLLER_GEN) rbac:roleName=manager-role crd webhook paths="./..." output:crd:artifacts:config=config/crd/bases

.PHONY: generate
generate: $(CONTROLLER_GEN) ## Generate code containing DeepCopy, DeepCopyInto, and DeepCopyObject method implementations.
	$(CONTROLLER_GEN) object:headerFile="hack/boilerplate.go.txt" paths="./..."

.PHONY: fmt
fmt: $(GOIMPORTS) ## Run go fmt against code.
	$(GOIMPORTS) -local github.com/projectsveltos -w .
	go fmt ./...

.PHONY: vet
vet: ## Run go vet against code.
	go vet ./...

.PHONY: lint
lint: $(GOLANGCI_LINT) generate ## Lint codebase
	$(GOLANGCI_LINT) run -v --fast=false --max-issues-per-linter 0 --max-same-issues 0 --timeout 5m	

ifeq ($(shell go env GOOS),darwin) # Use the darwin/amd64 binary until an arm64 version is available
KUBEBUILDER_ASSETS ?= $(shell $(SETUP_ENVTEST) use --use-env -p path --arch amd64 $(KUBEBUILDER_ENVTEST_KUBERNETES_VERSION))
else
KUBEBUILDER_ASSETS ?= $(shell $(SETUP_ENVTEST) use --use-env -p path $(KUBEBUILDER_ENVTEST_KUBERNETES_VERSION))
endif

##@ TESTING

# K8S_VERSION for the Kind cluster can be set as environment variable. If not defined,
# this default value is used
ifndef K8S_VERSION
K8S_VERSION := v1.24.2
endif

KIND_CONFIG ?= kind-cluster.yaml
CONTROL_CLUSTER_NAME ?= sveltos-management
WORKLOAD_CLUSTER_NAME ?= sveltos-management-workload
TIMEOUT ?= 10m
KIND_CLUSTER_YAML ?= test/sveltos-management-workload.yaml
NUM_NODES ?= 3

.PHONY: test
test: manifests generate fmt vet $(SETUP_ENVTEST) ## Run uts.
	KUBEBUILDER_ASSETS="$(KUBEBUILDER_ASSETS)" go test $(shell go list ./... |grep -v test/fv |grep -v pkg/deployer/fake |grep -v test/helpers) $(TEST_ARGS) -coverprofile cover.out 

.PHONY: kind-test
kind-test: test create-cluster fv ## Build docker image; start kind cluster; load docker image; install all cluster api components and run fv

.PHONY: fv
fv: $(GINKGO) ## Run Sveltos Controller tests using existing cluster
	cd test/fv; ../../$(GINKGO) -nodes $(NUM_NODES) --label-filter='FV' --v --trace --randomize-all

.PHONY: create-cluster
create-cluster: $(KIND) $(CLUSTERCTL) $(KUBECTL) $(ENVSUBST) ## Create a new kind cluster designed for development
	$(MAKE) create-control-cluster

	@echo wait for capd-system pod
	$(KUBECTL) wait --for=condition=Available deployment/capd-controller-manager -n capd-system --timeout=$(TIMEOUT)

	@echo "Create a workload cluster"
	$(KUBECTL) apply -f $(KIND_CLUSTER_YAML)

	@echo "Start projectsveltos"
	$(MAKE) deploy-projectsveltos

	@echo "wait for cluster to be provisioned"
	$(KUBECTL) wait cluster sveltos-management-workload -n default --for=jsonpath='{.status.phase}'=Provisioned --timeout=$(TIMEOUT)

	@echo "sleep allowing control plane to be ready"
	sleep 60

	@echo "get kubeconfig to access workload cluster"
	$(KIND) get kubeconfig --name $(WORKLOAD_CLUSTER_NAME) > test/fv/workload_kubeconfig

	@echo "install calico on workload cluster"
	$(KUBECTL) --kubeconfig=./test/fv/workload_kubeconfig apply -f https://docs.projectcalico.org/v3.21/manifests/calico.yaml

	@echo wait for calico pod
	$(KUBECTL) --kubeconfig=./test/fv/workload_kubeconfig wait --for=condition=Available deployment/calico-kube-controllers -n kube-system --timeout=$(TIMEOUT)

.PHONY: delete-cluster
delete-cluster: $(KIND) ## Deletes the kind cluster $(CONTROL_CLUSTER_NAME)
	$(KUBECTL) delete cluster $(WORKLOAD_CLUSTER_NAME) --wait=true --ignore-not-found=true 
	$(KIND) delete cluster --name $(CONTROL_CLUSTER_NAME)

### fv helpers

# In order to avoid this error
# Error: failed to read "cluster-template-development.yaml" from provider's repository "infrastructure-docker": failed to get GitHub release v1.2.0: rate limit for github api has been reached. 
# Please wait one hour or get a personal API token and assign it to the GITHUB_TOKEN environment variable
#
# add this target. It needs to be run only when changing cluster-api version. create-cluster target uses the output of this command which is stored within repo
# It requires control cluster to exist
create-clusterapi-kind-cluster-yaml: $(CLUSTERCTL) 
	KUBERNETES_VERSION=v1.24.2 SERVICE_CIDR=["10.225.0.0/16"] POD_CIDR=["10.220.0.0/16"] $(CLUSTERCTL) generate cluster $(WORKLOAD_CLUSTER_NAME) --flavor development \
		--control-plane-machine-count=1 \
  		--worker-machine-count=1 > $(KIND_CLUSTER_YAML)

create-control-cluster:
	sed -e "s/K8S_VERSION/$(K8S_VERSION)/g"  test/$(KIND_CONFIG) > test/$(KIND_CONFIG).tmp
	$(KIND) create cluster --name=$(CONTROL_CLUSTER_NAME) --config test/$(KIND_CONFIG).tmp
	@echo "Create control cluster with docker as infrastructure provider"
	CLUSTER_TOPOLOGY=true $(CLUSTERCTL) init --infrastructure docker

deploy-projectsveltos: $(KUSTOMIZE)
	# Load projectsveltos image into cluster
	@echo 'Load projectsveltos image into cluster'
	$(MAKE) load-image
	
	# Install projectsveltos controller-manager components
	@echo 'Install projectsveltos controller-manager components'
	cd config/manager && ../../$(KUSTOMIZE) edit set image controller=${IMG}
	$(KUSTOMIZE) build config/default | $(ENVSUBST) | $(KUBECTL) apply -f-

	@echo "Waiting for projectsveltos controller-manager to be available..."
	$(KUBECTL) wait --for=condition=Available deployment/fm-controller-manager -n projectsveltos --timeout=$(TIMEOUT)

set-manifest-image:
	$(info Updating kustomize image patch file for manager resource)
	sed -i'' -e 's@image: .*@image: '"${MANIFEST_IMG}:$(MANIFEST_TAG)"'@' ./config/default/manager_image_patch.yaml

set-manifest-pull-policy:
	$(info Updating kustomize pull policy file for manager resource)
	sed -i'' -e 's@imagePullPolicy: .*@imagePullPolicy: '"$(PULL_POLICY)"'@' ./config/default/manager_pull_policy.yaml

##@ Build

.PHONY: build
build: generate fmt vet ## Build manager binary.
	go build -o bin/manager main.go

.PHONY: run
run: manifests generate fmt vet ## Run a controller from your host.
	go run ./main.go

.PHONY: docker-build
docker-build: ## Build docker image with the manager.
	docker build -t $(CONTROLLER_IMG)-$(ARCH):$(TAG) .
	MANIFEST_IMG=$(CONTROLLER_IMG)-$(ARCH) MANIFEST_TAG=$(TAG) $(MAKE) set-manifest-image
	$(MAKE) set-manifest-pull-policy

.PHONY: docker-push
docker-push: ## Push docker image with the manager.
	docker push $(CONTROLLER_IMG)-$(ARCH):$(TAG)

.PHONY: load-image
load-image: docker-build $(KIND)
	$(KIND) load docker-image $(CONTROLLER_IMG)-$(ARCH):$(TAG) --name $(CONTROL_CLUSTER_NAME)

##@ Deployment

ifndef ignore-not-found
  ignore-not-found = false
endif

.PHONY: install
install: manifests $(KUSTOMIZE) ## Install CRDs into the K8s cluster specified in ~/.kube/config.
	$(KUSTOMIZE) build config/crd | kubectl apply -f -

.PHONY: uninstall
uninstall: manifests $(KUSTOMIZE) ## Uninstall CRDs from the K8s cluster specified in ~/.kube/config. Call with ignore-not-found=true to ignore resource not found errors during deletion.
	$(KUSTOMIZE) build config/crd | kubectl delete --ignore-not-found=$(ignore-not-found) -f -

.PHONY: deploy
deploy: manifests $(KUSTOMIZE) ## Deploy controller to the K8s cluster specified in ~/.kube/config.
	cd config/manager && $(KUSTOMIZE) edit set image controller=${IMG}
	$(KUSTOMIZE) build config/default | kubectl apply -f -

.PHONY: undeploy
undeploy: s $(KUSTOMIZE) ## Undeploy controller from the K8s cluster specified in ~/.kube/config. Call with ignore-not-found=true to ignore resource not found errors during deletion.
	$(KUSTOMIZE) build config/default | kubectl delete --ignore-not-found=$(ignore-not-found) -f -
