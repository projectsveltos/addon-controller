
# Image URL to use all building/pushing image targets
IMG ?= controller:latest
# KUBEBUILDER_ENVTEST_KUBERNETES_VERSION refers to the version of kubebuilder assets to be downloaded by envtest binary.
KUBEBUILDER_ENVTEST_KUBERNETES_VERSION = 1.34.0

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
IMAGE_NAME ?= addon-controller
ARCH ?= $(shell go env GOARCH)

OS ?= $(shell uname -s)
OS := $(shell echo $(OS) | tr '[:upper:]' '[:lower:]')
K8S_LATEST_VER ?= $(shell curl -s https://storage.googleapis.com/kubernetes-release/release/stable.txt)
export CONTROLLER_IMG ?= $(REGISTRY)/$(IMAGE_NAME)
TAG ?= main

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
BIN_DIR := bin
TOOLS_BIN_DIR := $(abspath $(TOOLS_DIR)/$(BIN_DIR))

GOBUILD=go build

GENERATED_FILES:=./manifest/manifest.yaml

## Tool Binaries
CONTROLLER_GEN := $(TOOLS_BIN_DIR)/controller-gen
ENVSUBST := $(TOOLS_BIN_DIR)/envsubst
GOIMPORTS := $(TOOLS_BIN_DIR)/goimports
GOLANGCI_LINT := $(TOOLS_BIN_DIR)/golangci-lint
GOVULNCHECK := $(TOOLS_BIN_DIR)/govulncheck
GINKGO := $(TOOLS_BIN_DIR)/ginkgo
SETUP_ENVTEST := $(TOOLS_BIN_DIR)/setup_envs
CLUSTERCTL := $(TOOLS_BIN_DIR)/clusterctl
KIND := $(TOOLS_BIN_DIR)/kind
KUBECTL := $(TOOLS_BIN_DIR)/kubectl

GOVULNCHECK_VERSION := "v1.1.4"
GOLANGCI_LINT_VERSION := "v2.4.0"
CLUSTERCTL_VERSION := "v1.11.0"

KUSTOMIZE_VER := v5.7.0
KUSTOMIZE_BIN := kustomize
KUSTOMIZE := $(abspath $(TOOLS_BIN_DIR)/$(KUSTOMIZE_BIN)-$(KUSTOMIZE_VER))
KUSTOMIZE_PKG := sigs.k8s.io/kustomize/kustomize/v5
$(KUSTOMIZE): # Build kustomize from tools folder.
	CGO_ENABLED=0 GOBIN=$(TOOLS_BIN_DIR) $(GO_INSTALL) $(KUSTOMIZE_PKG) $(KUSTOMIZE_BIN) $(KUSTOMIZE_VER)

CONVERSION_GEN_VER := v0.32.0
CONVERSION_GEN_BIN := conversion-gen
# We are intentionally using the binary without version suffix, to avoid the version
# in generated files.
CONVERSION_GEN := $(abspath $(TOOLS_BIN_DIR)/$(CONVERSION_GEN_BIN))
CONVERSION_GEN_PKG := k8s.io/code-generator/cmd/conversion-gen

.PHONY: $(CONVERSION_GEN_BIN)
$(CONVERSION_GEN_BIN): $(CONVERSION_GEN) ## Build a local copy of conversion-gen.

## We are forcing a rebuilt of conversion-gen via PHONY so that we're always using an up-to-date version.
## We can't use a versioned name for the binary, because that would be reflected in generated files.
.PHONY: $(CONVERSION_GEN)
$(CONVERSION_GEN): # Build conversion-gen from tools folder.
	GOBIN=$(TOOLS_BIN_DIR) $(GO_INSTALL) $(CONVERSION_GEN_PKG) $(CONVERSION_GEN_BIN) $(CONVERSION_GEN_VER)

SETUP_ENVTEST_VER := release-0.22
SETUP_ENVTEST_BIN := setup-envtest
SETUP_ENVTEST := $(abspath $(TOOLS_BIN_DIR)/$(SETUP_ENVTEST_BIN)-$(SETUP_ENVTEST_VER))
SETUP_ENVTEST_PKG := sigs.k8s.io/controller-runtime/tools/setup-envtest
setup-envtest: $(SETUP_ENVTEST) ## Set up envtest (download kubebuilder assets)
	@echo KUBEBUILDER_ASSETS=$(KUBEBUILDER_ASSETS)

$(SETUP_ENVTEST_BIN): $(SETUP_ENVTEST) ## Build a local copy of setup-envtest.

$(SETUP_ENVTEST): # Build setup-envtest from tools folder.
	GOBIN=$(TOOLS_BIN_DIR) $(GO_INSTALL) $(SETUP_ENVTEST_PKG) $(SETUP_ENVTEST_BIN) $(SETUP_ENVTEST_VER)

$(CONTROLLER_GEN): $(TOOLS_DIR)/go.mod # Build controller-gen from tools folder.
	cd $(TOOLS_DIR); $(GOBUILD) -tags=tools -o $(subst $(TOOLS_DIR)/hack/tools/,,$@) sigs.k8s.io/controller-tools/cmd/controller-gen

$(ENVSUBST): $(TOOLS_DIR)/go.mod # Build envsubst from tools folder.
	cd $(TOOLS_DIR); $(GOBUILD) -tags=tools -o $(subst $(TOOLS_DIR)/hack/tools/,,$@) github.com/a8m/envsubst/cmd/envsubst

$(GOLANGCI_LINT):
	cd $(TOOLS_DIR); ./get-golangci-lint.sh $(GOLANGCI_LINT_VERSION)

$(GOVULNCHECK):
	cd $(TOOLS_DIR); ./get-govulncheck.sh $(GOVULNCHECK_VERSION)

$(GOIMPORTS):
	cd $(TOOLS_DIR); $(GOBUILD) -tags=tools -o $(subst $(TOOLS_DIR)/hack/tools/,,$@) golang.org/x/tools/cmd/goimports

$(GINKGO): $(TOOLS_DIR)/go.mod
	cd $(TOOLS_DIR) && $(GOBUILD) -tags tools -o $(subst $(TOOLS_DIR)/hack/tools/,,$@) github.com/onsi/ginkgo/v2/ginkgo

$(KIND): $(TOOLS_DIR)/go.mod
	cd $(TOOLS_DIR) && $(GOBUILD) -tags tools -o $(subst $(TOOLS_DIR)/hack/tools/,,$@) sigs.k8s.io/kind

$(CLUSTERCTL): $(TOOLS_DIR)/go.mod ## Build clusterctl binary
	curl -L https://github.com/kubernetes-sigs/cluster-api/releases/download/$(CLUSTERCTL_VERSION)/clusterctl-$(OS)-$(ARCH) -o $@
	chmod +x $@

$(KUBECTL):
	curl -L https://storage.googleapis.com/kubernetes-release/release/$(K8S_LATEST_VER)/bin/$(OS)/$(ARCH)/kubectl -o $@
	chmod +x $@

.PHONY: tools
tools: $(CONTROLLER_GEN) $(ENVSUBST) $(KUSTOMIZE) $(SETUP_ENVTEST) $(GOLANGCI_LINT) $(GOIMPORTS) $(GINKGO) $(CLUSTERCTL) $(KIND) $(KUBECTL) ## build all tools

.PHONY: clean
clean: ## Remove all built tools
	rm -rf $(TOOLS_BIN_DIR)/*
	rm -rf $(GENERATED_FILES)

##@ Development

.PHONY: manifests
manifests: $(CONTROLLER_GEN) $(KUSTOMIZE) $(ENVSUBST) fmt generate ## Generate WebhookConfiguration, ClusterRole and CustomResourceDefinition objects.
	$(CONTROLLER_GEN) rbac:roleName=controller-role crd webhook paths="./..." output:crd:artifacts:config=config/crd/bases
	MANIFEST_IMG=$(CONTROLLER_IMG) MANIFEST_TAG=$(TAG) $(MAKE) set-manifest-image
	$(KUSTOMIZE) build config/default | $(ENVSUBST) > manifest/manifest.yaml
	./scripts/extract_deployment-shard.sh manifest/manifest.yaml manifest/deployment-shard.yaml
	./scripts/extract_deployment-agentless.sh manifest/manifest.yaml manifest/deployment-agentless.yaml

.PHONY: generate
generate: $(CONTROLLER_GEN) ## Generate code containing DeepCopy, DeepCopyInto, and DeepCopyObject method implementations.
	go generate
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
	$(GOLANGCI_LINT) run -v --max-issues-per-linter 0 --max-same-issues 0 --timeout 5m

.PHONY: govulncheck
govulncheck: $(GOVULNCHECK)
	$(GOVULNCHECK) ./...

.PHONY: check-manifests
check-manifests: manifests ## Verify manifests file is up to date
	test `git status --porcelain $(GENERATED_FILES) | grep -cE '(^\?)|(^ M)'` -eq 0 || (echo "The manifest file changed, please 'make manifests' and commit the results"; exit 1)

ifeq ($(shell go env GOOS),darwin) # Use the darwin/amd64 binary until an arm64 version is available
KUBEBUILDER_ASSETS ?= $(shell $(SETUP_ENVTEST) use --use-env -p path --arch amd64 $(KUBEBUILDER_ENVTEST_KUBERNETES_VERSION))
else
KUBEBUILDER_ASSETS ?= $(shell $(SETUP_ENVTEST) use --use-env -p path $(KUBEBUILDER_ENVTEST_KUBERNETES_VERSION))
endif

##@ TESTING

# K8S_VERSION for the Kind cluster can be set as environment variable. If not defined,
# this default value is used
ifndef K8S_VERSION
K8S_VERSION := v1.33.0
endif

KIND_CONFIG ?= kind-cluster.yaml
CONTROL_CLUSTER_NAME ?= sveltos-management
KIND_PULLMODE_CLUSTER1 ?= pullmode-kind-cluster1.yaml
KIND_PULLMODE_CLUSTER2 ?= pullmode-kind-cluster2.yaml
SVELTOS_NETWORK_NAME ?= sveltos-kind-network
WORKLOAD_CLUSTER_NAME ?= clusterapi-workload
TMP_FILE ?= test/pullmode-kubeconfig_data.tmp
TIMEOUT ?= 10m
WORKLOAD_CLUSTER_YAML ?= test/$(WORKLOAD_CLUSTER_NAME).yaml
NUM_NODES ?= 6

.PHONY: quickstart
quickstart:  ## start kind cluster; install all cluster api components; create a capi cluster; install projectsveltos
	$(MAKE) create-control-cluster

	$(MAKE) create-workload-cluster

	# this is needed fopr projectsveltos metrics
	$(KUBECTL) apply -f test/quickstart/servicemonitor_crd.yaml

	@echo "Start projectsveltos"
	$(KUBECTL) apply -f https://raw.githubusercontent.com/projectsveltos/sveltos/$(TAG)/manifest/manifest.yaml

	sleep 5

	@echo "Waiting for projectsveltos addon-controller to be available..."
	$(KUBECTL) wait --for=condition=Available deployment/addon-controller -n projectsveltos --timeout=$(TIMEOUT)

.PHONY: test
test: | check-manifests generate fmt vet $(SETUP_ENVTEST) ## Run uts.
	KUBEBUILDER_ASSETS="$(KUBEBUILDER_ASSETS)" go test $(shell go list ./... |grep -v test/fv |grep -v test/helpers) $(TEST_ARGS) -coverprofile cover.out

.PHONY: kind-test
kind-test: test create-cluster fv ## Build docker image; start kind cluster; load docker image; install all cluster api components and run fv

.PHONY: fv
fv: $(KUBECTL) $(GINKGO) ## Run Sveltos Controller tests using existing cluster
	cd test/fv; $(GINKGO) -nodes $(NUM_NODES) --label-filter='FV' --v --trace --randomize-all

.PHONY: new-fv
new-fv: $(KUBECTL) $(GINKGO) ## Run Sveltos Controller tests using existing cluster
	cd test/fv; $(GINKGO) -nodes $(NUM_NODES) --label-filter='NEW-FV' --v --trace --randomize-all

.PHONY: fv-pullmode
fv-pullmode: $(KUBECTL) $(GINKGO) ## Run Sveltos Controller tests using existing cluster
	cd test/fv; $(GINKGO) -nodes $(NUM_NODES) --label-filter='PULLMODE' --v --trace --randomize-all

.PHONY: new-fv-pullmode
new-fv-pullmode: $(KUBECTL) $(GINKGO) ## Run Sveltos Controller tests using existing cluster
	cd test/fv; $(GINKGO) -nodes $(NUM_NODES) --label-filter='NEW-FV-PULLMODE' --v --trace --randomize-all


.PHONY: fv-sharding
fv-sharding: $(KUBECTL) $(GINKGO) ## Run Sveltos Controller tests using existing cluster
	$(KUBECTL) patch cluster clusterapi-workload  -n default --type json -p '[{ "op": "add", "path": "/metadata/annotations/sharding.projectsveltos.io~1key", "value": "shard1" }]'
	sed -e "s/{{.SHARD}}/shard1/g"  manifest/deployment-shard.yaml > test/addon-controller-deployment-shard.yaml
	$(KUBECTL) apply -f test/addon-controller-deployment-shard.yaml
	rm -f test/addon-controller-deployment-shard.yaml
	cd test/fv; $(GINKGO) -nodes $(NUM_NODES) --label-filter='FV' --v --trace --randomize-all

.PHONY: fv-agentless
fv-agentless: $(KUBECTL) $(GINKGO) ## Run Sveltos Controller tests using existing cluster
	$(KUBECTL) apply -f test/drift-detection-mgmt_cluster_common_manifest.yaml
	$(KUBECTL) apply -f manifest/drift_detection_manager_rbac.yaml
	$(KUBECTL) apply -f manifest/deployment-agentless.yaml
	sleep 60
	@echo "Waiting for projectsveltos addon-controller to be available..."
	$(KUBECTL) wait --for=condition=Available deployment/addon-controller -n projectsveltos --timeout=$(TIMEOUT)
	cd test/fv; $(GINKGO) -nodes $(NUM_NODES) --label-filter='FV' --v --trace --randomize-all

.PHONY: create-cluster
create-cluster: $(KIND) $(CLUSTERCTL) $(KUBECTL) $(ENVSUBST) ## Create a new kind cluster designed for development
	$(MAKE) create-control-cluster

	@echo "Start projectsveltos"
	$(MAKE) deploy-projectsveltos

	$(MAKE) create-workload-cluster

	@echo "prepare configMap with kustomize files"
	$(KUBECTL) create configmap kustomize --from-file=test/kustomize.tar.gz

	@echo "prepare configMap with flux resources"
	$(KUBECTL) create configmap install-flux --from-file=test/flux-install.yaml

	@echo apply reloader CRD to managed cluster
	$(KUBECTL) --kubeconfig=./test/fv/workload_kubeconfig apply -f https://raw.githubusercontent.com/projectsveltos/libsveltos/$(TAG)/manifests/apiextensions.k8s.io_v1_customresourcedefinition_reloaders.lib.projectsveltos.io.yaml

.PHONY: delete-cluster
delete-cluster: $(KIND) ## Deletes the kind cluster $(CONTROL_CLUSTER_NAME)
	$(KIND) delete cluster --name $(CONTROL_CLUSTER_NAME)
	$(KIND) delete cluster --name $(WORKLOAD_CLUSTER_NAME)

### fv helpers

# In order to avoid this error
# Error: failed to read "cluster-template-development.yaml" from provider's repository "infrastructure-docker": failed to get GitHub release v1.2.0: rate limit for github api has been reached.
# Please wait one hour or get a personal API token and assign it to the GITHUB_TOKEN environment variable
#
# add this target. It needs to be run only when changing cluster-api version. create-cluster target uses the output of this command which is stored within repo
# It requires control cluster to exist. So first "make create-control-cluster" then run this target.
# Once generated, add label to cluster env: fv
# Once generated, remove
#      enforce: "{{ .podSecurityStandard.enforce }}"
#      enforce-version: "latest"
create-clusterapi-kind-cluster-yaml: $(CLUSTERCTL)
	CLUSTER_TOPOLOGY=true ENABLE_POD_SECURITY_STANDARD="true" KUBERNETES_VERSION=$(K8S_VERSION) SERVICE_CIDR=["10.225.0.0/16"] POD_CIDR=["10.220.0.0/16"] $(CLUSTERCTL) generate cluster $(WORKLOAD_CLUSTER_NAME) --flavor development \
		--control-plane-machine-count=1 \
		--worker-machine-count=2 > $(WORKLOAD_CLUSTER_YAML)

create-control-cluster: $(KIND) $(CLUSTERCTL) $(KUBECTL)
	sed -e "s/K8S_VERSION/$(K8S_VERSION)/g"  test/$(KIND_CONFIG) > test/$(KIND_CONFIG).tmp
	$(KIND) create cluster --name=$(CONTROL_CLUSTER_NAME) --config test/$(KIND_CONFIG).tmp
	@echo "Create control cluster with docker as infrastructure provider"
	CLUSTER_TOPOLOGY=true $(CLUSTERCTL) init --core cluster-api --bootstrap kubeadm --control-plane kubeadm --infrastructure docker

	@echo wait for capd-system pod
	$(KUBECTL) wait --for=condition=Available deployment/capd-controller-manager -n capd-system --timeout=$(TIMEOUT)
	$(KUBECTL) wait --for=condition=Available deployment/capi-kubeadm-control-plane-controller-manager -n capi-kubeadm-control-plane-system --timeout=$(TIMEOUT)
	$(KUBECTL) wait --for=condition=Available deployment/capi-kubeadm-bootstrap-controller-manager -n capi-kubeadm-bootstrap-system --timeout=$(TIMEOUT)

	@echo "sleep allowing webhook to be ready"
	sleep 10

create-workload-cluster: $(KIND) $(KUBECTL)
	@echo "Create a workload cluster"
	$(KUBECTL) apply -f $(WORKLOAD_CLUSTER_YAML)

	@echo "wait for cluster to be provisioned"
	$(KUBECTL) wait cluster $(WORKLOAD_CLUSTER_NAME) -n default --for=jsonpath='{.status.phase}'=Provisioned --timeout=$(TIMEOUT)

	@echo "sleep allowing control plane to be ready"
	sleep 100

	@echo "get kubeconfig to access workload cluster"
	$(KIND) get kubeconfig --name $(WORKLOAD_CLUSTER_NAME) > test/fv/workload_kubeconfig

	@echo "install calico on workload cluster"
	$(KUBECTL) --kubeconfig=./test/fv/workload_kubeconfig apply -f https://raw.githubusercontent.com/projectcalico/calico/v3.29.0/manifests/calico.yaml

	@echo wait for calico pod
	$(KUBECTL) --kubeconfig=./test/fv/workload_kubeconfig wait --for=condition=Available deployment/calico-kube-controllers -n kube-system --timeout=$(TIMEOUT)

create-cluster-pullmode: $(KIND) $(KUBECTL) $(ENVSUBST) $(KUSTOMIZE)
	docker network rm $(SVELTOS_NETWORK_NAME) 2>/dev/null || true
	docker network create $(SVELTOS_NETWORK_NAME)

	echo "Create the management cluster"
	sed -e "s/K8S_VERSION/$(K8S_VERSION)/g"  test/$(KIND_PULLMODE_CLUSTER1) > test/$(KIND_PULLMODE_CLUSTER1).tmp
	$(KIND) create cluster --name=$(CONTROL_CLUSTER_NAME) --config test/$(KIND_PULLMODE_CLUSTER1).tmp

	@echo "Start projectsveltos"
	$(MAKE) deploy-projectsveltos

	@echo "prepare configMap with kustomize files"
	$(KUBECTL) create configmap kustomize --from-file=test/kustomize.tar.gz

	@echo "prepare configMap with flux resources"
	$(KUBECTL) create configmap install-flux --from-file=test/flux-install.yaml

	@echo "Deploy a Job in cluster1 that creates the kubeconfig sveltos-applier needs to connect to"
	$(KUBECTL) apply -f https://raw.githubusercontent.com/projectsveltos/pullmode-prepper/refs/heads/main/manifest.yaml
	$(KUBECTL) wait --for=condition=complete job/register-pullmode-cluster-job -n projectsveltos --timeout=$(TIMEOUT)
	@echo "Waiting for KUBECONFIG_DATA to be available..."
	@temp_kubeconfig_data="" ; \
	while true; do \
		temp_kubeconfig_data="$$($(KUBECTL) get configmap clusterapi-workload -o jsonpath='{.data.kubeconfig}' 2>/dev/null |base64 -d || echo '')"; \
		echo "KUBECONFIG DATA: $$temp_kubeconfig_data"; \
  		if [ -n "$$temp_kubeconfig_data" ]; then \
  			echo "$$temp_kubeconfig_data" > $(TMP_FILE); \
  			break; \
  		fi; \
  		echo "KUBECONFIG_DATA not found. Retrying in 5 seconds..."; \
  		sleep 5; \
	done

	echo "Create the managed cluster"
	sed -e "s/K8S_VERSION/$(K8S_VERSION)/g"  test/$(KIND_PULLMODE_CLUSTER2) > test/$(KIND_PULLMODE_CLUSTER2).tmp
	$(KIND) create cluster --name=$(WORKLOAD_CLUSTER_NAME) --config test/$(KIND_PULLMODE_CLUSTER2).tmp

	docker network connect $(SVELTOS_NETWORK_NAME) $(CONTROL_CLUSTER_NAME)-control-plane
	docker network connect $(SVELTOS_NETWORK_NAME) $(WORKLOAD_CLUSTER_NAME)-control-plane

	echo "Create a Secret with cluster1 kubeconfig in cluster2"
	$(KUBECTL) create ns projectsveltos
	$(KUBECTL) create secret generic -n projectsveltos $(WORKLOAD_CLUSTER_NAME)-sveltos-kubeconfig --from-file=kubeconfig=$(TMP_FILE)

	echo "Deploy sveltos-applier in cluster2"
	$(KUBECTL) apply -f test/pullmode-sveltosapplier.yaml

	@echo apply reloader CRD to managed cluster
	$(KUBECTL) apply -f https://raw.githubusercontent.com/projectsveltos/libsveltos/$(TAG)/manifests/apiextensions.k8s.io_v1_customresourcedefinition_reloaders.lib.projectsveltos.io.yaml

	@echo "Waiting for projectsveltos sveltos-applier to be available..."
	$(KUBECTL) wait --for=condition=Available deployment/sveltos-applier-manager -n projectsveltos --timeout=$(TIMEOUT)

	@echo "get kubeconfig to access workload cluster"
	$(KIND) get kubeconfig --name $(WORKLOAD_CLUSTER_NAME) > test/fv/workload_kubeconfig

	@echo "Switching to cluster1..."
	$(KUBECTL) config use-context kind-$(CONTROL_CLUSTER_NAME)

deploy-projectsveltos: $(KUSTOMIZE)
	# Load projectsveltos image into cluster
	@echo 'Load projectsveltos image into cluster'
	$(MAKE) load-image

	@echo 'Install libsveltos CRDs'
	$(KUBECTL) apply -f https://raw.githubusercontent.com/projectsveltos/libsveltos/$(TAG)/manifests/apiextensions.k8s.io_v1_customresourcedefinition_debuggingconfigurations.lib.projectsveltos.io.yaml
	$(KUBECTL) apply -f https://raw.githubusercontent.com/projectsveltos/libsveltos/$(TAG)/manifests/apiextensions.k8s.io_v1_customresourcedefinition_resourcesummaries.lib.projectsveltos.io.yaml
	$(KUBECTL) apply -f https://raw.githubusercontent.com/projectsveltos/libsveltos/$(TAG)/manifests/apiextensions.k8s.io_v1_customresourcedefinition_sveltosclusters.lib.projectsveltos.io.yaml
	$(KUBECTL) apply -f https://raw.githubusercontent.com/projectsveltos/libsveltos/$(TAG)/manifests/apiextensions.k8s.io_v1_customresourcedefinition_clustersets.lib.projectsveltos.io.yaml
	$(KUBECTL) apply -f https://raw.githubusercontent.com/projectsveltos/libsveltos/$(TAG)/manifests/apiextensions.k8s.io_v1_customresourcedefinition_sets.lib.projectsveltos.io.yaml
	$(KUBECTL) apply -f https://raw.githubusercontent.com/projectsveltos/libsveltos/$(TAG)/manifests/apiextensions.k8s.io_v1_customresourcedefinition_reloaders.lib.projectsveltos.io.yaml
	$(KUBECTL) apply -f https://raw.githubusercontent.com/projectsveltos/libsveltos/$(TAG)/manifests/apiextensions.k8s.io_v1_customresourcedefinition_configurationgroups.lib.projectsveltos.io.yaml
	$(KUBECTL) apply -f https://raw.githubusercontent.com/projectsveltos/libsveltos/$(TAG)/manifests/apiextensions.k8s.io_v1_customresourcedefinition_configurationbundles.lib.projectsveltos.io.yaml
	$(KUBECTL) apply -f https://raw.githubusercontent.com/projectsveltos/libsveltos/$(TAG)/manifests/apiextensions.k8s.io_v1_customresourcedefinition_sveltoslicenses.lib.projectsveltos.io.yaml

	# Install projectsveltos addon-controller components
	@echo 'Install projectsveltos addon-controller components'
	cd config/manager && $(KUSTOMIZE) edit set image controller=${IMG}
	$(KUSTOMIZE) build config/default | $(ENVSUBST) | $(KUBECTL) apply -f-

	$(KUBECTL) apply -f https://raw.githubusercontent.com/projectsveltos/libsveltos/$(TAG)/configuration/default-debuggingconfiguration.yaml

	# Install sveltoscluster-manager
	$(KUBECTL) apply -f https://raw.githubusercontent.com/projectsveltos/sveltoscluster-manager/$(TAG)/manifest/manifest.yaml

	@echo "Waiting for projectsveltos addon-controller to be available..."
	$(KUBECTL) wait --for=condition=Available deployment/addon-controller -n projectsveltos --timeout=$(TIMEOUT)

prepare-configmap-with-kustomize: $(KUBECTL)
	mkdir tmp; cd tmp; git clone git@github.com:gianlucam76/kustomize.git; \
	tar -czf ../test/kustomize.tar.gz -C kustomize/helloWorldWithOverlays .; \
	cd ..; rm -rf tmp

set-manifest-image:
	$(info Updating kustomize image patch file for manager resource)
	sed -i'' -e 's@image: .*@image: '"docker.io/${MANIFEST_IMG}:$(MANIFEST_TAG)"'@' ./config/default/manager_image_patch.yaml
	sed -i'' -e 's@--version=.*@--version=$(TAG)"@' ./config/default/manager_auth_proxy_patch.yaml

set-manifest-pull-policy:
	$(info Updating kustomize pull policy file for manager resource)
	sed -i'' -e 's@imagePullPolicy: .*@imagePullPolicy: '"$(PULL_POLICY)"'@' ./config/default/manager_pull_policy.yaml

##@ Build

.PHONY: build
build: generate fmt vet ## Build manager binary.
	go build -o bin/manager cmd/main.go

.PHONY: run
run: manifests generate fmt vet ## Run a controller from your host.
	go run ./cmd/main.go

.PHONY: docker-build
docker-build: ## Build docker image with the manager.
	go generate
	docker build --load --build-arg BUILDOS=linux --build-arg TARGETARCH=amd64 -t $(CONTROLLER_IMG):$(TAG) .
	MANIFEST_IMG=$(CONTROLLER_IMG) MANIFEST_TAG=$(TAG) $(MAKE) set-manifest-image
	$(MAKE) set-manifest-pull-policy

.PHONY: docker-push
docker-push: ## Push docker image with the manager.
	docker push $(CONTROLLER_IMG):$(TAG)

.PHONY: docker-buildx
docker-buildx: ## docker build for multiple arch and push to docker hub
	docker buildx build --push --platform linux/amd64,linux/arm64 -t $(CONTROLLER_IMG):$(TAG) .
	docker buildx build --push --platform linux/amd64,linux/arm64 -t $(CONTROLLER_IMG)-git:$(TAG) -f Dockerfile_WithGit .

.PHONY: load-image
load-image: docker-build $(KIND)
	$(KIND) load docker-image $(CONTROLLER_IMG):$(TAG) --name $(CONTROL_CLUSTER_NAME)

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
	$(KUSTOMIZE) build config/default | $(ENVSUBST) | $(KUBECTL)  apply -f -

.PHONY: undeploy
undeploy: s $(KUSTOMIZE) ## Undeploy controller from the K8s cluster specified in ~/.kube/config. Call with ignore-not-found=true to ignore resource not found errors during deletion.
	$(KUSTOMIZE) build config/default | kubectl delete --ignore-not-found=$(ignore-not-found) -f -


define get-digest-drift-detection
$(shell skopeo inspect --format '{{.Digest}}' "docker://projectsveltos/drift-detection-manager:${TAG}" --override-os="linux" --override-arch="amd64" --override-variant="v8" 2>/dev/null)
endef

define get-digest-sveltos-applier
$(shell skopeo inspect --format '{{.Digest}}' "docker://projectsveltos/sveltos-applier:main" --override-os="linux" --override-arch="amd64" --override-variant="v8" 2>/dev/null)
endef

drift-detection-manager:
	@echo "Downloading drift detection manager yaml"
	$(eval digest :=$(call get-digest-drift-detection))
	@echo "image digest is $(get-digest-drift-detection)"
	curl -L -H "Authorization: token $$GITHUB_PAT" https://raw.githubusercontent.com/projectsveltos/drift-detection-manager/$(TAG)/manifest/manifest.yaml -o ./pkg/drift-detection/drift-detection-manager.yaml
	sed -i '' -e "s#image: docker.io/projectsveltos/drift-detection-manager:${TAG}#image: docker.io/projectsveltos/drift-detection-manager@${digest}#g" ./pkg/drift-detection/drift-detection-manager.yaml
	curl -L -H "Authorization: token $$GITHUB_PAT" https://raw.githubusercontent.com/projectsveltos/drift-detection-manager/$(TAG)/manifest/mgmt_cluster_manifest.yaml -o ./pkg/drift-detection/drift-detection-manager-in-mgmt-cluster.yaml
	sed -i '' -e "s#image: docker.io/projectsveltos/drift-detection-manager:${TAG}#image: docker.io/projectsveltos/drift-detection-manager@${digest}#g" ./pkg/drift-detection/drift-detection-manager-in-mgmt-cluster.yaml
	cd pkg/drift-detection; go generate
	@echo "Downloading drift detection manager common yaml for agentless fv"
	curl -L -H "Authorization: token $$GITHUB_PAT" https://raw.githubusercontent.com/projectsveltos/drift-detection-manager/$(TAG)/manifest/mgmt_cluster_common_manifest.yaml -o ./test/drift-detection-mgmt_cluster_common_manifest.yaml

sveltos-applier:
	@echo "Downloading sveltos applier yaml"
	$(eval digest :=$(call get-digest-sveltos-applier))
	@echo "image digest is $(get-digest-sveltos-applier)"
	curl -L -H "Authorization: token $$GITHUB_PAT" https://raw.githubusercontent.com/projectsveltos/sveltos-applier/main/manifest/manifest.yaml -o ./test/pullmode-sveltosapplier.yaml
	sed -i '' -e "s#image: docker.io/projectsveltos/sveltos-applier:main#image: docker.io/projectsveltos/sveltos-applier@${digest}#g" ./test/pullmode-sveltosapplier.yaml
	sed -i '' -e "s#cluster-namespace=#cluster-namespace=default#g" ./test/pullmode-sveltosapplier.yaml
	sed -i '' -e "s#cluster-name=#cluster-name=clusterapi-workload#g" ./test/pullmode-sveltosapplier.yaml
	sed -i '' -e "s#cluster-type=#cluster-type=sveltos#g" ./test/pullmode-sveltosapplier.yaml
	sed -i '' -e "s#secret-with-kubeconfig=#secret-with-kubeconfig=clusterapi-workload-sveltos-kubeconfig#g" ./test/pullmode-sveltosapplier.yaml
