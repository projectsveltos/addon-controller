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

package controllers_test

const (
	// Controller names
	testControllerNameSummary      = "clustersummary"
	testControllerNameProfile      = "clusterprofile"
	testControllerNameProfileShort = "profile"

	// Kubernetes API versions and kinds
	testV1APIVersion     = "v1"
	testConfigAPIVersion = "config.projectsveltos.io/v1beta1"
	testKindDeployment   = "Deployment"
	testKindConfigMap    = "ConfigMap"
	testKindSecret       = "Secret"

	// Helm chart versions and releases
	testChartVersion100         = "v1.0.0"
	testChartVersion250         = "v2.5.0"
	testChartVersion253         = "v2.5.3"
	testChartVersion119         = "1.19.0"
	testChartVersion2114        = "21.1.4"
	testReleaseNameNginxLatest  = "nginx-latest"
	testReleaseNameCalico       = "calico"
	testReleaseNameKyverno      = "kyverno"
	testReleaseNamePrometheus   = "prometheus"
	testReleaseNameGrafana      = "grafana"
	testReleaseNameContour      = "contour-latest"
	testRepoURLBitnami          = "https://charts.bitnami.com/bitnami"
	testChartNameBitnamiContour = "bitnami/contour"

	// Label keys and values
	testEnvLabelKey    = "env"
	testQAValue        = "qa"
	testDCLabelKey     = "dc"
	testRegionKey      = "region"
	testClusterNameKey = "cluster-name"

	// Annotation values
	testOkValue   = "ok"
	testTrueValue = "true"

	// Field and map key names
	testValueKey         = "value"
	testValue1           = "value1"
	testValue2           = "value2"
	testStatusField      = "status"
	testServiceKey       = "service"
	testImageKey         = "image"
	testRepositoryKey    = "repository"
	testNginxRepo        = "nginx"
	testNginxLatestImage = "nginx:latest"
	testReplicaCountKey  = "replicaCount"
	testTagKey           = "tag"
	testPortKey          = "port"
	testPasswordKey      = "password"
	testChangeKey        = "change"
	testVersionKey       = "version"
	testAppsGroup        = "apps"
	testCertManagerGroup = "cert-manager.io"
	testDeleteVerb       = "delete"
	testReadyReplicasKey = "readyReplicas"
	testReplicasKey      = "replicas"

	// Label values
	testProductionValue = "production"
	testEngValue        = "eng"
	testTestingValue    = "testing"

	// Kubernetes resource names
	testCertificateRequestsResource = "certificaterequests"
	testDeploymentsResource         = "deployments"
	testResourceSummaryCRDName      = "resourcesummaries.lib.projectsveltos.io"
	testWatchedKey                  = "watched"

	// JSON paths and watch fields
	testSpecReplicasPath    = "/spec/replicas"
	testDataWatchedField    = "data.watched"
	testStatusReadyReplicas = "status.readyReplicas"
	testStatusNonExistent   = "status.nonExistent"

	// Drift detection
	testDriftDetectionManagerName = "drift-detection-manager"
	testControlPlaneLabel         = "control-plane"

	// Namespace
	defaultNamespace = "default"

	// Template strings
	testClusterNameTemplate      = "{{ .Cluster.metadata.name }}"
	testClusterNamePatchTemplate = "{{ .Cluster.metadata.name }}-patch"
	testClusterFullNameTemplate  = "{{ .Cluster.metadata.namespace }}-{{ .Cluster.metadata.name }}"
	testRegionLabelTemplate      = `{{ index .Cluster.metadata.labels "region" }}`

	// Misc
	testClusterRoleKindV1 = "ClusterRole.v1.rbac.authorization.k8s.io"

	// Multiline patch
	testEnvLabelPatch = `- op: add
  path: /metadata/labels/environment
  value: production`

	// Verbs and resources
	testCreateVerb         = "create"
	testGetVerb            = "get"
	testNamespacesResource = "namespaces"

	// Field keys
	testSpecKey = "spec"
)
