LOCALBIN ?= $(shell pwd)/bin
export LOCALBIN
$(LOCALBIN):
	mkdir -p $(LOCALBIN)

TEMPLATES_DIR := charts
CHARTS_PACKAGE_DIR ?= $(LOCALBIN)/charts
EXTENSION_CHARTS_PACKAGE_DIR ?= $(LOCALBIN)/charts/extensions
$(EXTENSION_CHARTS_PACKAGE_DIR): | $(LOCALBIN)
	mkdir -p $(EXTENSION_CHARTS_PACKAGE_DIR)
$(CHARTS_PACKAGE_DIR): | $(LOCALBIN)
	rm -rf $(CHARTS_PACKAGE_DIR)
	mkdir -p $(CHARTS_PACKAGE_DIR)

KCM_NAMESPACE ?= kcm-system
CONTAINER_TOOL ?= docker
KIND_NETWORK ?= kind
REGISTRY_NAME ?= kcm-local-registry
REGISTRY_PORT ?= 5001
REGISTRY_REPO ?= oci://127.0.0.1:$(REGISTRY_PORT)/charts
REGISTRY_IS_OCI = $(shell echo $(REGISTRY_REPO) | grep -q oci && echo true || echo false)
REGISTRY_PLAIN_HTTP ?= true

TEMPLATE_FOLDERS = $(patsubst $(TEMPLATES_DIR)/%,%,$(wildcard $(TEMPLATES_DIR)/*))

KIND_CLUSTER_NAME ?= kcm-dev

define set_local_registry
	$(eval $@_VALUES = $(1))
	$(YQ) eval -i '.k0rdent-istio.repo.spec.url = "oci://$(REGISTRY_NAME):5000/charts"' ${$@_VALUES}
	$(YQ) eval -i '.k0rdent-istio.repo.spec.type = "oci"' ${$@_VALUES}
endef

dev:
	mkdir -p dev
lint-chart-%:
	$(HELM) dependency update $(TEMPLATES_DIR)/$*
	$(HELM) lint --strict $(TEMPLATES_DIR)/$* --set global.lint=true

package-chart-%: lint-chart-%
	$(HELM) package --destination $(CHARTS_PACKAGE_DIR) $(TEMPLATES_DIR)/$*


.PHONY: registry-deploy
registry-deploy:
	@if [ ! "$$($(CONTAINER_TOOL) ps -aq -f name=$(REGISTRY_NAME))" ]; then \
		echo "Starting new local registry container $(REGISTRY_NAME)"; \
		$(CONTAINER_TOOL) run -d --restart=always -p "127.0.0.1:$(REGISTRY_PORT):5000" --network bridge --name "$(REGISTRY_NAME)" registry:2; \
	fi; \
	if [ "$$($(CONTAINER_TOOL) inspect -f='{{json .NetworkSettings.Networks.$(KIND_NETWORK)}}' $(REGISTRY_NAME))" = 'null' ]; then \
		$(CONTAINER_TOOL) network connect $(KIND_NETWORK) $(REGISTRY_NAME); \
	fi

.PHONY: helm-package
helm-package: $(CHARTS_PACKAGE_DIR) $(EXTENSION_CHARTS_PACKAGE_DIR)
	rm -rf $(CHARTS_PACKAGE_DIR)
	@make $(patsubst %,package-chart-%,$(TEMPLATE_FOLDERS))

.PHONY: helm-push
helm-push: helm-package
	@if [ ! $(REGISTRY_IS_OCI) ]; then \
	    repo_flag="--repo"; \
	fi; \
	if [ $(REGISTRY_PLAIN_HTTP) = "true" ]; \
	then plain_http_flag="--plain-http"; \
	else plain_http_flag=""; \
	fi; \
	for chart in $(CHARTS_PACKAGE_DIR)/*.tgz; do \
		base=$$(basename $$chart .tgz); \
		chart_version=$$(echo $$base | grep -o "v\{0,1\}[0-9]\+\.[0-9]\+\.[0-9].*"); \
		chart_name="$${base%-"$$chart_version"}"; \
		echo "Verifying if chart $$chart_name, version $$chart_version already exists in $(REGISTRY_REPO)"; \
		if $(REGISTRY_IS_OCI); then \
			chart_exists=$$($(HELM) pull $$repo_flag $(REGISTRY_REPO)/$$chart_name --version $$chart_version --destination /tmp 2>&1 | grep "not found" || true); \
		else \
			chart_exists=$$($(HELM) pull $$repo_flag $(REGISTRY_REPO) $$chart_name --version $$chart_version --destination /tmp 2>&1 | grep "not found" || true); \
		fi; \
		if [ -z "$$chart_exists" ]; then \
			echo "Chart $$chart_name version $$chart_version already exists in the repository."; \
		fi; \
		if $(REGISTRY_IS_OCI); then \
			echo "Pushing $$chart to $(REGISTRY_REPO)"; \
			$(HELM) push $${plain_http_flag} "$$chart" $(REGISTRY_REPO); \
		else \
			$(HELM) repo add kcm $(REGISTRY_REPO); \
			echo "Pushing $$chart to $(REGISTRY_REPO)"; \
			$(HELM) cm-push -f "$$chart" $(REGISTRY_REPO) --insecure; \
		fi; \
	done

.PHONY: dev-istio-deploy
dev-istio-deploy: dev istio-operator-docker-build ## Deploy k0rdent-istio helm chart to the K8s cluster specified in ~/.kube/config
	cp -f $(TEMPLATES_DIR)/k0rdent-istio/values.yaml dev/k0rdent-istio-values.yaml
	@$(call set_local_registry, "dev/k0rdent-istio-values.yaml")
	@$(YQ) eval -i '.operator.image.registry = "docker.io/library"' dev/k0rdent-istio-values.yaml # See `load docker-image`
	@$(YQ) eval -i '.operator.image.repository = "istio-operator-controller"' dev/k0rdent-istio-values.yaml
	$(HELM_UPGRADE) --create-namespace -n istio-system k0rdent-istio ./charts/k0rdent-istio -f dev/k0rdent-istio-values.yaml

.PHONY: istio-operator-docker-build
istio-operator-docker-build: yq ## Build istio-operator controller docker image
	cd istio-operator && make docker-build YQ=$(YQ)
	@istio_version=v$$($(YQ) .version $(TEMPLATES_DIR)/k0rdent-istio/Chart.yaml); \
	$(CONTAINER_TOOL) tag istio-operator-controller istio-operator-controller:$$istio_version; \
	if command -v kind >/dev/null && kind get clusters | grep -q "^$(KIND_CLUSTER_NAME)$$"; then \
		kind load docker-image istio-operator-controller:$$istio_version --name $(KIND_CLUSTER_NAME); \
	else \
		echo "Skipping kind image load: cluster '$(KIND_CLUSTER_NAME)' not found"; \
	fi

.PHONY: dev-adopted-deploy
dev-adopted-deploy: dev kind envsubst ## Create adopted cluster deployment
	@if ! $(KIND) get clusters | grep -q "^$(KIND_CLUSTER_NAME)$$"; then \
		if [ -n "$(KIND_CONFIG_PATH)" ]; then \
			$(KIND) create cluster -n $(KIND_CLUSTER_NAME) --config "$(KIND_CONFIG_PATH)" --wait 1m; \
		else \
			$(KIND) create cluster -n $(KIND_CLUSTER_NAME) --wait 1m; \
		fi \
	fi
	$(KUBECTL) config use kind-kcm-dev
	NAMESPACE=$(KCM_NAMESPACE) \
	KUBECONFIG_DATA=$$($(KIND) get kubeconfig --internal -n $(KIND_CLUSTER_NAME) | base64 -w 0) \
	KIND_CLUSTER_NAME=$(KIND_CLUSTER_NAME) \
	$(ENVSUBST) -no-unset -i demo/creds/adopted-credentials.yaml \
	| $(KUBECTL) apply -f -

## Tool Binaries
HELM ?= $(LOCALBIN)/helm-$(HELM_VERSION)
HELM_UPGRADE = $(HELM) upgrade -i --reset-values --wait
export HELM HELM_UPGRADE
KIND ?= $(LOCALBIN)/kind-$(KIND_VERSION)
YQ ?= $(LOCALBIN)/yq-$(YQ_VERSION)
ENVSUBST ?= $(LOCALBIN)/envsubst-$(ENVSUBST_VERSION)
KUBECTL ?= kubectl

export YQ

## Tool Versions
HELM_VERSION ?= v3.18.5
YQ_VERSION ?= v4.44.2
KIND_VERSION ?= v0.27.0
ENVSUBST_VERSION ?= v1.4.2

.PHONY: envsubst
envsubst: $(ENVSUBST)
$(ENVSUBST): | $(LOCALBIN)
	$(call go-install-tool,$(ENVSUBST),github.com/a8m/envsubst/cmd/envsubst,${ENVSUBST_VERSION})


.PHONY: yq
yq: $(YQ) ## Download yq locally if necessary.
$(YQ): | $(LOCALBIN)
	$(call go-install-tool,$(YQ),github.com/mikefarah/yq/v4,${YQ_VERSION})

.PHONY: kind
kind: $(KIND) ## Download kind locally if necessary.
$(KIND): | $(LOCALBIN)
	$(call go-install-tool,$(KIND),sigs.k8s.io/kind,${KIND_VERSION})

.PHONY: helm
helm: $(HELM) ## Download helm locally if necessary.
HELM_INSTALL_SCRIPT ?= "https://raw.githubusercontent.com/helm/helm/master/scripts/get-helm-3"
$(HELM): | $(LOCALBIN)
	rm -f $(LOCALBIN)/helm-*
	curl -s --fail $(HELM_INSTALL_SCRIPT) | USE_SUDO=false HELM_INSTALL_DIR=$(LOCALBIN) DESIRED_VERSION=$(HELM_VERSION) BINARY_NAME=helm-$(HELM_VERSION) PATH="$(LOCALBIN):$(PATH)" bash

.PHONY: cli-install
cli-install: yq helm kind ## Install the necessary CLI tools for deployment, development and testing.

# go-install-tool will 'go install' any package with custom target and name of binary, if it doesn't exist
# $1 - target path with name of binary (ideally with version)
# $2 - package url which can be installed
# $3 - specific version of package
define go-install-tool
@[ -f $(1) ] || { \
set -e; \
package=$(2)@$(3) ;\
echo "Downloading $${package}" ;\
GOBIN=$(LOCALBIN) go install $${package} ;\
if [ ! -f $(1) ]; then mv -f "$$(echo "$(1)" | sed "s/-$(3)$$//")" $(1); fi ;\
}
endef
