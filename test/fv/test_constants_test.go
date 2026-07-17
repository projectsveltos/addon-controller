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
	kyvernoRepoURL              = "https://kyverno.github.io/kyverno/"
	kyvernoChartName            = "kyverno/kyverno"
	kyvernoNamespace            = "kyverno"
	admissionControllerDeplName = "kyverno-admission-controller"
	kyvernoVersion382           = "v3.8.2"
	kyvernoVersion382S          = "3.8.2"
	kyvernoVersion381           = "v3.8.1"
	kyvernoVersion381S          = "3.8.1"
	kyvernoVersion372           = "v3.7.2"
	kyvernoVersion372S          = "3.7.2"
	kyvernoVersion371           = "v3.7.1"
	kyvernoVersion371S          = "3.7.1"
	kyvernoLatestRelease        = "kyverno-latest"

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

	// cert-manager GPG keyring for provenance verification (downloaded from
	// https://cert-manager.io/public-keys/cert-manager-keyring-2021-09-20-1020CF3C033D4F35BAE1C19E1226061C665DF13E.gpg)
	certManagerKeyringBase64 = "mQINBGFUc7EBEACxlkppohGM6nNuJZggMo7KvL1cYGoB+97WI4aa3lU7WOmM2gSx" +
		"5PrN7CjMd9ZMzuizFrLLZaM3wb5BXf0yVPdXGj3X/fWD5lyrwpy/Eb6IHwZFoP2k" +
		"2AuaCGiu/55gOI9gw7/5+GwsibIblYSg9y0x4IEPu6Zn8Da33pT0NDFUOpBtMeWw" +
		"2W52ejWnXCyBGd9QzdAd7E2WMO8mDkVwMyh0KlUulE29vDSVslV/JsniyInmj8yZ" +
		"fPLRAImnD7Iwiq+oxzyqAonXKigm25NUXi3j+W4/4Qb+hUiJJYIV5Eyph1sndH4I" +
		"mL7gs3u8U1hAzVD83A4HnZG15pHvCeqZwHOpJgrfYwzzF08JDm2KhbCc9NFs9m7O" +
		"xmxGYmwoE70lSsFh0Oe1lfXFjmDhA1K5/GJCAmAlJTsThUYy9qATMHUnO86voD+o" +
		"0UI7VnomwDePDdwktXWtId/Gp9JcEAJMsN/ps49ZEgtErJ1MkDq4so0qajX+cWbe" +
		"O93m0co6PLhUBAsn9XH3AGwACItr7Tq966c7PcAn2w2HfAunbVEvXW71O44pEGKk" +
		"T9NiwFinyOkjJY9cNJX4lwP9NmqhPzLRnpLR1qZ9Saym4r0NNAUvZyul5qolIb1E" +
		"D4649kvEYPUgxRFOw8kHuReE+2Zx56KsGfCxVxEccOK1W3yg7/Hha6zVzQARAQAB" +
		"tERjZXJ0LW1hbmFnZXIgTWFpbnRhaW5lcnMgPGNlcnQtbWFuYWdlci1tYWludGFp" +
		"bmVyc0Bnb29nbGVncm91cHMuY29tPokCIgQTAQoAFgUCYVRzsQkQEiYGHGZd8T4C" +
		"GwICGQEAAIFdD/9F5/hLLE09A96gTT7EXMZQk6OpNw83u8pUVdHF8qSwGIbSLvOZ" +
		"5C4wdM/NP6APNwZCOBn8SoW9wG/4NU286lN8KfhOOqEBJA2SqIifanRTvFPWqNr8" +
		"iEi5zWkKGAIaR+m13PKQddYL2DDdxN0W2aw8ddN/GZIBA7iiGvphJVOp/3NiskqZ" +
		"j15PT9Elb4O5kSJoqItjwSbOBtZXYb1G6wkui1rXoZGJG20xYdt9Yze8IpRPtuXr" +
		"LR/8zff0xDJ3HNDi8LhWvPW4Vzt5tNpO7zBdskSTlOACWmBz9RZcFVddtPG5Zp7m" +
		"1Yv4I6Oewl1/yQDAfzR1i1GkSjHFHS6CSHl3qYUcXIHxhTog01LbTE95vvbT0mKA" +
		"wpdM/WKsgGBYZMVyINbsDVKZmh+wYNQ4KBhEyL5FfGkGJ4uUx9SHnN3NrLBn7PkW" +
		"ox8ciYD0wIxHe+kS8J8KgnTdl5myfIxI2BmTFcaRxEYv31mvi8lW9cxwjxbdcgIw" +
		"kZjC8/aT2EM+U5T+OAGL+CZauzPN4al3oPDQBdGYKMFyiSVNSyHXuI7CCbY9Hw+" +
		"mo9A9Z6pHaisykC0PbErd+2DqaD8eyvwg8y0YJcpoCK0b3Vhia/zk9haL5t7lxvz" +
		"+QpjFTiz4Qke377RFHJGGAhMNZ7+WkmlzB9qksxLdiQGn8vYyZCQTf6WNHw=="

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
