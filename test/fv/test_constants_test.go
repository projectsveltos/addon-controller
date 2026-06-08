/*
Copyright 2025. projectsveltos.io. All rights reserved.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package fv_test

const (
	// Kyverno chart details
	kyvernoRepoURL       = "https://kyverno.github.io/kyverno/"
	kyvernoChartName     = "kyverno/kyverno"
	kyvernoVersion352    = "v3.5.2"
	kyvernoVersion352S   = "3.5.2"
	kyvernoVersion351    = "v3.5.1"
	kyvernoLatestRelease = "kyverno-latest"

	// Prometheus community chart details
	prometheusCommunityURL  = "https://prometheus-community.github.io/helm-charts"
	prometheusCommunityName = "prometheus-community"
	prometheusChartName     = "prometheus-community/prometheus"
	prometheusVersion2524   = "25.24.0"
	prometheusVersion2739   = "27.39.0"
	prometheusRelease       = "prometheus"

	// Grafana chart details
	grafanaRepoName    = "grafana"
	grafanaChartName   = "grafana/grafana"
	grafanaVersion1136 = "11.3.6"
	grafanaVersion1000 = "10.0.0"

	// Jetstack/cert-manager chart details
	jetstackURL              = "https://charts.jetstack.io"
	jetstackName             = "jetstack"
	jetstackCertManagerChart = "jetstack/cert-manager"
	certManager              = "cert-manager"

	// Kong chart details
	kongRepoName    = "kong"
	kongVersion2510 = "2.51.0"

	// External-DNS chart details
	externalDNSURL       = "https://kubernetes-sigs.github.io/external-dns/"
	externalDNSRepoName  = "external-dns"
	externalDNSChartName = "external-dns/external-dns"
	externalDNSVersion   = "1.20.0"
	externaldnsGroup     = "externaldns.k8s.io"

	// Bitnami chart details
	bitnamiURL  = "https://charts.bitnami.com/bitnami"
	bitnamiName = "bitnami"

	// NGINX chart details
	nginxLatestRelease = "nginx-latest"
	nginxNamespace     = "nginx"
	nginxVersion222    = "2.2.2"

	// Release names
	airflowRelease = "airflow"
	flinkRelease   = "flink"

	// Kubernetes API versions
	apiVersionV1       = "v1"
	apiVersionV1alpha1 = "v1alpha1"

	// Kubernetes kinds
	kindDeployment     = "Deployment"
	kindNamespace      = "Namespace"
	kindSecret         = "Secret"
	kindConfigMap      = "ConfigMap"
	kindClusterRole    = "ClusterRole"
	kindServiceAccount = "ServiceAccount"
	kindPod            = "Pod"

	// Kubernetes groups
	appsGroupName = "apps"
	rbacAuthGroup = "rbac.authorization.k8s.io"

	// Annotation values
	annotationOkValue   = "ok"
	annotationTrueValue = "true"

	// Template strings
	clusterNameTemplate      = "{{ .Cluster.metadata.name }}"
	clusterNamespaceTemplate = "{{ .Cluster.metadata.namespace }}"

	// Resource types
	resourceTypeSecrets = "secrets"

	// MariaDB chart details
	mariadbOperatorURL     = "https://helm.mariadb.com/mariadb-operator"
	mariadbOperatorName    = "mariadb-operator"
	mariadbOperatorChart   = "mariadb-operator/mariadb-operator"
	mariadbVersion0351     = "0.35.1"
	mariadbRelease         = "mariadb"
	prometheusVersion2584  = "25.8.4"
	externalDNSVersion1182 = "v1.18.2"
	crdsEnabledValues      = "crds:\n  enabled: true"

	// CloudNative-PG chart details
	cloudnativePGURL     = "https://cloudnative-pg.github.io/charts"
	cloudnativePGName    = "cloudnative-pg"
	cloudnativePGChart   = "cloudnative-pg/cloudnative-pg"
	cloudnativePGVersion = "0.26.0"
	cnpgRelease          = "cnpg"
	cnpgSystem           = "cnpg-system"

	// Vault OCI chart details
	ociVaultURL       = "oci://registry-1.docker.io/bitnamicharts"
	ociVaultRepoName  = "oci-vault"
	vaultChartName    = "vault"
	vaultVersion160   = "1.6.0"
	vaultVersion150   = "1.5.0"
	vaultInjectorName = "vault-injector"

	// Resource names
	nginxDeploymentName    = "nginx-deployment"
	nginxServiceName       = "nginx-service"
	kongServiceAccountName = "kong-serviceaccount"

	// Kubernetes resource types
	serviceAccountsResource = "serviceaccounts"

	// Kubernetes kinds
	kindService = "Service"
	kindJob     = "Job"

	// Kubernetes groups
	batchGroupName = "batch"

	// Label/annotation keys
	clusterNameKey = "cluster-name"

	// Annotation values for profile
	productionValue          = "production"
	promotionNameAnnotation  = "config.projectsveltos.io/promotionname"
	promotionVerifAnnotation = "config.projectsveltos.io/promotion-verification"

	// Helm chart versions
	leaveHelmVersion = "7.1.1"

	// Other names
	fluxSystemName  = "flux-system"
	contourRelease  = "contour"
	matchAllPattern = ".*"

	// Template patterns (without spaces before }})
	clusterNamespaceTrimTemplate = "{{ .Cluster.metadata.namespace}}"
	patchesCMNameTemplate        = "{{ .Cluster.metadata.name}}-patches"

	// Misc
	addonDeplName            = "addon-controller"
	addonControllerRoleExtra = "addon-controller-role-extra"
	templateCMName           = "template"
	watchfieldsResultName    = "watchfields-result"
	testValue1               = "value1"
	watchedFieldKey          = "watched"

	// Sveltos/cluster management
	defaultSveltosNamespace = "projectsveltos"
	clusterapiWorkloadName  = "clusterapi-workload"

	// MariaDB/CNPG deployment names
	mariadbOperatorDeployName = "mariadb-mariadb-operator"
	cnpgDeployName            = "cnpg-cloudnative-pg"

	// Spark chart
	sparkName       = "spark"
	sparkMasterName = "spark-master"

	// k0rdent catalog / ingress-nginx / postgres
	k0rdentCatalogURL    = "oci://ghcr.io/k0rdent/catalog/charts"
	ingressNginxName     = "ingress-nginx"
	postgresOperatorName = "postgres-operator"

	// Helm chart versions
	externalDNSVersion1170 = "1.17.0"
	argocdChartVersion     = "3.35.4"
	wildflyVersion         = "2.4.0"
	kyvernoVersion370      = "v3.7.0"
	kyvernoVersion370S     = "3.7.0"
	kubePrometheusVersion  = "75.9.0"

	// ArgoCD chart
	argocdName       = "argocd"
	argocdServerName = "argocd-server"

	// Wildfly chart
	wildflyRepoURL   = "https://docs.wildfly.org/wildfly-charts/"
	wildflyName      = "wildfly"
	wildflyChartName = "wildfly/wildfly"

	// Kustomize resources
	flux2Name                = "flux2"
	kustomizeServiceName     = "the-service"
	kustomizeMapName         = "the-map"
	kustomizeProdServiceName = "production-the-service"
	kustomizeProdDeployName  = "production-the-deployment"
	kustomizeProdMapName     = "production-the-map"
)
