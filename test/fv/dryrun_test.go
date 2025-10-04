/*
Copyright 2022. projectsveltos.io. All rights reserved.

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

import (
	"context"
	"fmt"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/util/retry"
	"sigs.k8s.io/controller-runtime/pkg/client"

	configv1beta1 "github.com/projectsveltos/addon-controller/api/v1beta1"
	"github.com/projectsveltos/addon-controller/lib/clusterops"
	libsveltosv1beta1 "github.com/projectsveltos/libsveltos/api/v1beta1"
)

const (
	kongServiceAccount = `apiVersion: v1
kind: ServiceAccount
metadata:
  name: kong-serviceaccount
  namespace: kong
  `

	kongRole = `apiVersion: rbac.authorization.k8s.io/v1
kind: Role
metadata:
  name: kong-leader-election
  namespace: kong2
rules:
- apiGroups:
  - ""
  - coordination.k8s.io
  resources:
  - configmaps
  - leases
  verbs:
  - get
  - list
  - watch
  - create
  - update
  - patch
  - delete
- apiGroups:
  - ""
  resources:
  - events
  verbs:
  - create
  - patch
  `
)

var _ = Describe("DryRun", Serial, func() {
	const (
		namePrefix  = "dry-run-"
		certManager = "cert-manager"
	)

	It("Correctly reports helm chart that would be installed, uninstalled or have conflicts",
		Label("NEW-FV", "NEW-FV-PULLMODE", "EXTENDED"), func() {
			configMapNs := randomString()
			Byf("Create configMap's namespace %s", configMapNs)
			ns := &corev1.Namespace{
				ObjectMeta: metav1.ObjectMeta{
					Name: configMapNs,
				},
			}
			Expect(k8sClient.Create(context.TODO(), ns)).To(Succeed())

			Byf("Create a configMap with kong ServiceAccount")
			kongSAConfigMap := createConfigMapWithPolicy(configMapNs, namePrefix+randomString(), kongServiceAccount)
			Expect(k8sClient.Create(context.TODO(), kongSAConfigMap)).To(Succeed())

			Byf("Create a ClusterProfile in Continuous syncMode matching Cluster %s/%s", kindWorkloadCluster.GetNamespace(), kindWorkloadCluster.GetName())
			clusterProfile := getClusterProfile(namePrefix, map[string]string{key: value})
			clusterProfile.Spec.SyncMode = configv1beta1.SyncModeContinuous
			Expect(k8sClient.Create(context.TODO(), clusterProfile)).To(Succeed())

			verifyClusterProfileMatches(clusterProfile)

			verifyClusterSummary(clusterops.ClusterProfileLabelName,
				clusterProfile.Name, &clusterProfile.Spec, kindWorkloadCluster.GetNamespace(),
				kindWorkloadCluster.GetName(), getClusterType())

			Byf("Update ClusterProfile %s to reference ConfigMap with Kong ServiceAccount %s/%s",
				clusterProfile.Name, kongSAConfigMap.Namespace, kongSAConfigMap.Name)
			currentClusterProfile := &configv1beta1.ClusterProfile{}

			err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
				Expect(k8sClient.Get(context.TODO(),
					types.NamespacedName{Name: clusterProfile.Name}, currentClusterProfile)).To(Succeed())
				currentClusterProfile.Spec.PolicyRefs = []configv1beta1.PolicyRef{
					{
						Kind:      string(libsveltosv1beta1.ConfigMapReferencedResourceKind),
						Namespace: kongSAConfigMap.Namespace,
						Name:      kongSAConfigMap.Name,
					},
				}
				return k8sClient.Update(context.TODO(), currentClusterProfile)
			})
			Expect(err).To(BeNil())

			Byf("Update ClusterProfile %s to deploy mysql helm chart", clusterProfile.Name)
			err = retry.RetryOnConflict(retry.DefaultRetry, func() error {
				Expect(k8sClient.Get(context.TODO(),
					types.NamespacedName{Name: clusterProfile.Name}, currentClusterProfile)).To(Succeed())
				currentClusterProfile.Spec.HelmCharts = []configv1beta1.HelmChart{
					{
						RepositoryURL:    "https://helm.mariadb.com/mariadb-operator",
						RepositoryName:   "mariadb-operator",
						ChartName:        "mariadb-operator/mariadb-operator",
						ChartVersion:     "0.35.1",
						ReleaseName:      "mariadb",
						ReleaseNamespace: "mariadb",
						HelmChartAction:  configv1beta1.HelmChartActionInstall,
					},
				}
				return k8sClient.Update(context.TODO(), currentClusterProfile)
			})
			Expect(err).To(BeNil())

			Expect(k8sClient.Get(context.TODO(),
				types.NamespacedName{Name: clusterProfile.Name}, currentClusterProfile)).To(Succeed())

			clusterSummary := verifyClusterSummary(clusterops.ClusterProfileLabelName,
				currentClusterProfile.Name, &currentClusterProfile.Spec,
				kindWorkloadCluster.GetNamespace(), kindWorkloadCluster.GetName(), getClusterType())

			Byf("Verifying ClusterSummary %s status is set to Deployed for Helm feature", clusterSummary.Name)
			verifyFeatureStatusIsProvisioned(kindWorkloadCluster.GetNamespace(), clusterSummary.Name, libsveltosv1beta1.FeatureHelm)

			Byf("Verifying ClusterSummary %s status is set to Deployed for Resource feature", clusterSummary.Name)
			verifyFeatureStatusIsProvisioned(kindWorkloadCluster.GetNamespace(), clusterSummary.Name, libsveltosv1beta1.FeatureResources)

			verifyDeployedGroupVersionKind(clusterProfile.Name)

			charts := []configv1beta1.Chart{
				{ReleaseName: "mariadb", ChartVersion: "0.35.1", Namespace: "mariadb"},
			}

			verifyClusterConfiguration(configv1beta1.ClusterProfileKind, clusterProfile.Name,
				clusterSummary.Spec.ClusterNamespace, clusterSummary.Spec.ClusterName, libsveltosv1beta1.FeatureHelm,
				nil, charts)

			policies := []policy{
				{kind: "ServiceAccount", name: "kong-serviceaccount", namespace: "kong", group: ""},
			}
			verifyClusterConfiguration(configv1beta1.ClusterProfileKind, clusterProfile.Name,
				clusterSummary.Spec.ClusterNamespace, clusterSummary.Spec.ClusterName, libsveltosv1beta1.FeatureResources,
				policies, nil)

			Byf("Create a configMap with kong Role")
			kongRoleConfigMap := createConfigMapWithPolicy(configMapNs, namePrefix+randomString(), kongRole)
			Expect(k8sClient.Create(context.TODO(), kongRoleConfigMap)).To(Succeed())

			Byf("Create a new ClusterProfile in DryRun syncMode matching Cluster %s/%s", kindWorkloadCluster.GetNamespace(), kindWorkloadCluster.GetName())
			dryRunClusterProfile := getClusterProfile(namePrefix, map[string]string{key: value})
			dryRunClusterProfile.Spec.SyncMode = configv1beta1.SyncModeDryRun
			Expect(k8sClient.Create(context.TODO(), dryRunClusterProfile)).To(Succeed())

			verifyClusterProfileMatches(dryRunClusterProfile)

			verifyClusterSummary(clusterops.ClusterProfileLabelName,
				dryRunClusterProfile.Name, &dryRunClusterProfile.Spec,
				kindWorkloadCluster.GetNamespace(), kindWorkloadCluster.GetName(), getClusterType())

			Byf("Update ClusterProfile %s to reference configMaps with Kong's configuration", dryRunClusterProfile.Name)
			err = retry.RetryOnConflict(retry.DefaultRetry, func() error {
				Expect(k8sClient.Get(context.TODO(),
					types.NamespacedName{Name: dryRunClusterProfile.Name},
					currentClusterProfile)).To(Succeed())
				currentClusterProfile.Spec.PolicyRefs = []configv1beta1.PolicyRef{
					{
						Kind:      string(libsveltosv1beta1.ConfigMapReferencedResourceKind),
						Namespace: configMapNs, Name: kongRoleConfigMap.Name,
					},
					{
						Kind:      string(libsveltosv1beta1.ConfigMapReferencedResourceKind),
						Namespace: configMapNs, Name: kongSAConfigMap.Name,
					},
				}
				return k8sClient.Update(context.TODO(), currentClusterProfile)
			})
			Expect(err).To(BeNil())

			Byf("Update ClusterProfile %s to reference some helm charts", dryRunClusterProfile.Name)
			err = retry.RetryOnConflict(retry.DefaultRetry, func() error {
				Expect(k8sClient.Get(context.TODO(),
					types.NamespacedName{Name: dryRunClusterProfile.Name},
					currentClusterProfile)).To(Succeed())
				currentClusterProfile.Spec.HelmCharts = []configv1beta1.HelmChart{
					{
						RepositoryURL:    "https://helm.mariadb.com/mariadb-operator",
						RepositoryName:   "mariadb-operator",
						ChartName:        "mariadb-operator/mariadb-operator",
						ChartVersion:     "0.36.0",
						ReleaseName:      "mariadb",
						ReleaseNamespace: "mariadb",
						HelmChartAction:  configv1beta1.HelmChartActionInstall,
					},
					{
						RepositoryURL:    "https://charts.jetstack.io",
						RepositoryName:   "jetstack",
						ChartName:        "jetstack/cert-manager",
						ChartVersion:     "v1.16.2",
						ReleaseName:      certManager,
						ReleaseNamespace: certManager,
						HelmChartAction:  configv1beta1.HelmChartActionInstall,
						Values: `crds:
  enabled: true`,
					},
					{
						RepositoryURL:    "https://cloudnative-pg.github.io/charts",
						RepositoryName:   "cloudnative-pg",
						ChartName:        "cloudnative-pg/cloudnative-pg",
						ChartVersion:     "0.22.1",
						ReleaseName:      "cnpg",
						ReleaseNamespace: "cnpg-system",
						HelmChartAction:  configv1beta1.HelmChartActionUninstall,
					},
				}
				return k8sClient.Update(context.TODO(), currentClusterProfile)
			})
			Expect(err).To(BeNil())

			dryRunClusterSummary := verifyClusterSummary(clusterops.ClusterProfileLabelName,
				currentClusterProfile.Name, &currentClusterProfile.Spec,
				kindWorkloadCluster.GetNamespace(), kindWorkloadCluster.GetName(), getClusterType())

			clusterReportName := fmt.Sprintf("%s--capi--%s", dryRunClusterProfile.Name, dryRunClusterSummary.Spec.ClusterName)
			if kindWorkloadCluster.GetKind() == libsveltosv1beta1.SveltosClusterKind {
				clusterReportName = fmt.Sprintf("%s--sveltos--%s", dryRunClusterProfile.Name, dryRunClusterSummary.Spec.ClusterName)
			}

			By("Verifying ClusterReport for policy reports")
			Eventually(func() error {
				currentClusterReport := &configv1beta1.ClusterReport{}
				err := k8sClient.Get(context.TODO(),
					types.NamespacedName{Namespace: dryRunClusterSummary.Spec.ClusterNamespace, Name: clusterReportName}, currentClusterReport)
				if err != nil {
					return err
				}
				// If not in DryRun, it would create Kong Role
				err = verifyResourceReport(currentClusterReport, "kong2", "kong-leader-election",
					"Role", "rbac.authorization.k8s.io", string(libsveltosv1beta1.CreateResourceAction))
				if err != nil {
					return err
				}
				// Another ClusterProfile is managing this, even though by referencing same ConfigMap this ClusterProfile is, so conflict.
				err = verifyResourceReport(currentClusterReport, "kong", "kong-serviceaccount",
					"ServiceAccount", "", string(libsveltosv1beta1.ConflictResourceAction))
				if err != nil {
					return err
				}
				return nil
			}, timeout, pollingInterval).Should(BeNil())

			By("Verifying ClusterReport for helm reports")
			verifyClusterReportForHelm(clusterReportName, dryRunClusterSummary, currentClusterProfile)

			verifyDeployedGroupVersionKind(clusterProfile.Name)

			Byf("Change ClusterProfile %s tier", dryRunClusterProfile.Name)
			setTier(dryRunClusterProfile.Name, int32(50))

			// Because of the new tier, the DryRun ClusterProfile wins every conflict. So action would
			// be upgrade for mysql helm chart and the Kong ServiceAccount (which were previously reported
			// as conflict)

			By("Verifying ClusterReport for helm reports")
			verifyClusterReportForHelm(clusterReportName, dryRunClusterSummary, currentClusterProfile)

			By("Verifying ClusterReport for policy reports")
			Eventually(func() error {
				currentClusterReport := &configv1beta1.ClusterReport{}
				err := k8sClient.Get(context.TODO(),
					types.NamespacedName{Namespace: dryRunClusterSummary.Spec.ClusterNamespace, Name: clusterReportName}, currentClusterReport)
				if err != nil {
					return err
				}
				// If not in DryRun, it would create Kong Role
				err = verifyResourceReport(currentClusterReport, "kong2", "kong-leader-election",
					"Role", "rbac.authorization.k8s.io", string(libsveltosv1beta1.CreateResourceAction))
				if err != nil {
					return err
				}
				// Another ClusterProfile is managing this, even though by referencing same ConfigMap this ClusterProfile is, so conflict.
				err = verifyResourceReport(currentClusterReport, "kong", "kong-serviceaccount",
					"ServiceAccount", "", string(libsveltosv1beta1.UpdateResourceAction))
				if err != nil {
					return err
				}
				return nil
			}, timeout, pollingInterval).Should(BeNil())

			Byf("Delete ClusterProfile %s", clusterProfile.Name)
			deleteClusterProfile(clusterProfile)

			Byf("Verifying ServiceAccount kong/kong-serviceaccount is removed from managed cluster")
			workloadClient, err := getKindWorkloadClusterKubeconfig()
			Expect(err).To(BeNil())
			Expect(workloadClient).ToNot(BeNil())

			currentServiceAccount := &corev1.ServiceAccount{}
			err = workloadClient.Get(context.TODO(),
				types.NamespacedName{Namespace: "kong", Name: "kong-serviceaccount"}, currentServiceAccount)
			Expect(err).ToNot(BeNil())
			Expect(apierrors.IsNotFound(err)).To(BeTrue())

			Byf("Changing syncMode to Continuous and HelmCharts (all install) for ClusterProfile %s", dryRunClusterProfile.Name)
			Expect(k8sClient.Get(context.TODO(), types.NamespacedName{Name: dryRunClusterProfile.Name}, currentClusterProfile)).To(Succeed())
			currentClusterProfile.Spec.SyncMode = configv1beta1.SyncModeContinuous
			currentClusterProfile.Spec.HelmCharts = []configv1beta1.HelmChart{
				{
					RepositoryURL:    "https://helm.mariadb.com/mariadb-operator",
					RepositoryName:   "mariadb-operator",
					ChartName:        "mariadb-operator/mariadb-operator",
					ChartVersion:     "0.36.0",
					ReleaseName:      "mariadb",
					ReleaseNamespace: "mariadb",
					HelmChartAction:  configv1beta1.HelmChartActionInstall,
				},
				{
					RepositoryURL:    "https://charts.jetstack.io",
					RepositoryName:   "jetstack",
					ChartName:        "jetstack/cert-manager",
					ChartVersion:     "v1.16.2",
					ReleaseName:      certManager,
					ReleaseNamespace: certManager,
					HelmChartAction:  configv1beta1.HelmChartActionInstall,
					Values: `crds:
  enabled: true`,
				},
				{
					RepositoryURL:    "https://cloudnative-pg.github.io/charts",
					RepositoryName:   "cloudnative-pg",
					ChartName:        "cloudnative-pg/cloudnative-pg",
					ChartVersion:     "0.22.1",
					ReleaseName:      "cnpg",
					ReleaseNamespace: "cnpg-system",
					HelmChartAction:  configv1beta1.HelmChartActionInstall,
				},
			}
			Expect(k8sClient.Update(context.TODO(), currentClusterProfile)).To(Succeed())

			verifyClusterSummary(clusterops.ClusterProfileLabelName,
				currentClusterProfile.Name, &currentClusterProfile.Spec,
				kindWorkloadCluster.GetNamespace(), kindWorkloadCluster.GetName(), getClusterType())

			currentDepl := &appsv1.Deployment{}
			Eventually(func() bool {
				Byf("Verifying ServiceAccount kong/kong-serviceaccount is deployed managed cluster")
				err = workloadClient.Get(context.TODO(),
					types.NamespacedName{Namespace: "kong", Name: "kong-serviceaccount"}, currentServiceAccount)
				if err != nil {
					return false
				}

				Byf("Verifying ServiceAccount cert-manager/cert-manager is deployed managed cluster")
				err = workloadClient.Get(context.TODO(),
					types.NamespacedName{Namespace: certManager, Name: certManager}, currentServiceAccount)
				if err != nil {
					return false
				}

				Byf("Verifying Deployment mariadb/mariadb-mariadb-operator is deployed managed cluster")
				err = workloadClient.Get(context.TODO(),
					types.NamespacedName{Namespace: "mariadb", Name: "mariadb-mariadb-operator"}, currentDepl)
				if err != nil {
					return false
				}

				Byf("Verifying Deployment cnpg-system/cnpg-cloudnative-pg is deployed managed cluster")
				err = workloadClient.Get(context.TODO(),
					types.NamespacedName{Namespace: "cnpg-system", Name: "cnpg-cloudnative-pg"}, currentDepl)
				return err == nil
			}, timeout, pollingInterval).Should(BeTrue())

			Byf("Verifying ClusterSummary %s status is set to Deployed for Resource feature", dryRunClusterSummary.Name)
			verifyFeatureStatusIsProvisioned(kindWorkloadCluster.GetNamespace(), dryRunClusterSummary.Name, libsveltosv1beta1.FeatureResources)

			Byf("Verifying ClusterSummary %s status is set to Deployed for Helm feature", dryRunClusterSummary.Name)
			verifyFeatureStatusIsProvisioned(kindWorkloadCluster.GetNamespace(), dryRunClusterSummary.Name, libsveltosv1beta1.FeatureHelm)

			Byf("Changing syncMode to DryRun and HelmCharts (some install, one uninstall) for ClusterProfile %s", dryRunClusterProfile.Name)
			Expect(k8sClient.Get(context.TODO(), types.NamespacedName{Name: dryRunClusterProfile.Name}, currentClusterProfile)).To(Succeed())
			currentClusterProfile.Spec.SyncMode = configv1beta1.SyncModeDryRun
			currentClusterProfile.Spec.PolicyRefs = []configv1beta1.PolicyRef{
				{
					Kind:      string(libsveltosv1beta1.ConfigMapReferencedResourceKind),
					Namespace: configMapNs, Name: kongRoleConfigMap.Name,
				},
			}
			currentClusterProfile.Spec.HelmCharts = []configv1beta1.HelmChart{
				{
					RepositoryURL:    "https://helm.mariadb.com/mariadb-operator",
					RepositoryName:   "mariadb-operator",
					ChartName:        "mariadb-operator/mariadb-operator",
					ChartVersion:     "0.36.0",
					ReleaseName:      "mariadb",
					ReleaseNamespace: "mariadb",
					HelmChartAction:  configv1beta1.HelmChartActionInstall,
					Values: `crds:
  enabled: true`,
				},
				{
					RepositoryURL:    "https://charts.jetstack.io",
					RepositoryName:   "jetstack",
					ChartName:        "jetstack/cert-manager",
					ChartVersion:     "v1.16.2",
					ReleaseName:      certManager,
					ReleaseNamespace: certManager,
					HelmChartAction:  configv1beta1.HelmChartActionInstall,
					Values: `crds:
  enabled: true`,
				},
				{
					RepositoryURL:    "https://cloudnative-pg.github.io/charts",
					RepositoryName:   "cloudnative-pg",
					ChartName:        "cloudnative-pg/cloudnative-pg",
					ChartVersion:     "0.22.1",
					ReleaseName:      "cnpg",
					ReleaseNamespace: "cnpg-system",
					HelmChartAction:  configv1beta1.HelmChartActionUninstall,
				},
			}
			Expect(k8sClient.Update(context.TODO(), currentClusterProfile)).To(Succeed())

			verifyClusterSummary(clusterops.ClusterProfileLabelName,
				currentClusterProfile.Name, &currentClusterProfile.Spec,
				kindWorkloadCluster.GetNamespace(), kindWorkloadCluster.GetName(), getClusterType())

			mariadDBRR := &libsveltosv1beta1.ResourceReport{
				Resource: libsveltosv1beta1.Resource{
					Kind: "CustomResourceDefinition",
					Name: "connections.k8s.mariadb.com",
				},
				Action: string(libsveltosv1beta1.CreateResourceAction),
			}

			certManagerRR := &libsveltosv1beta1.ResourceReport{
				Resource: libsveltosv1beta1.Resource{
					Kind: "ServiceAccount",
					Name: certManager,
				},
				Action: string(libsveltosv1beta1.NoResourceAction),
			}

			cloudNativePGRR := &libsveltosv1beta1.ResourceReport{
				Resource: libsveltosv1beta1.Resource{
					Kind: "ClusterRole",
					Name: "cnpg-cloudnative-pg-view",
				},
				Action: string(libsveltosv1beta1.DeleteResourceAction),
			}

			By("Verifying ClusterReport")
			Eventually(func() error {
				currentClusterReport := &configv1beta1.ClusterReport{}
				err = k8sClient.Get(context.TODO(),
					types.NamespacedName{Namespace: dryRunClusterSummary.Spec.ClusterNamespace, Name: clusterReportName}, currentClusterReport)
				if err != nil {
					return err
				}
				if kindWorkloadCluster.GetKind() == libsveltosv1beta1.SveltosClusterKind {
					return verifyReleaseResources(currentClusterReport, mariadDBRR, certManagerRR, cloudNativePGRR)
				} else {
					// ClusterProfile is managing mysql release but values changed
					err = verifyReleaseReport(currentClusterReport, currentClusterProfile.Spec.HelmCharts[0].ReleaseNamespace,
						currentClusterProfile.Spec.HelmCharts[0].ReleaseName, string(configv1beta1.UpdateHelmValuesAction))
					if err != nil {
						return err
					}
					// ClusterProfile is managing redis release
					err = verifyReleaseReport(currentClusterReport, currentClusterProfile.Spec.HelmCharts[1].ReleaseNamespace,
						currentClusterProfile.Spec.HelmCharts[1].ReleaseName, string(configv1beta1.NoHelmAction))
					if err != nil {
						return err
					}
					// postgres is installed and action is Uninstall
					err = verifyReleaseReport(currentClusterReport, currentClusterProfile.Spec.HelmCharts[2].ReleaseNamespace,
						currentClusterProfile.Spec.HelmCharts[2].ReleaseName, string(configv1beta1.UninstallHelmAction))
					if err != nil {
						return err
					}
				}
				return nil
			}, timeout, pollingInterval).Should(BeNil())

			By("Verifying ClusterReport for policy reports")
			Eventually(func() error {
				currentClusterReport := &configv1beta1.ClusterReport{}
				err = k8sClient.Get(context.TODO(),
					types.NamespacedName{Namespace: dryRunClusterSummary.Spec.ClusterNamespace, Name: clusterReportName}, currentClusterReport)
				if err != nil {
					return err
				}
				// If not in DryRun, it would create Kong Role
				err = verifyResourceReport(currentClusterReport, "kong2", "kong-leader-election",
					"Role", "rbac.authorization.k8s.io", string(libsveltosv1beta1.NoResourceAction))
				if err != nil {
					return err
				}
				// Previously installed this resource. Now not referencing the ConfigMap with this resource anymore.
				// So action would be delete
				err = verifyResourceReport(currentClusterReport, "kong", "kong-serviceaccount",
					"ServiceAccount", "", string(libsveltosv1beta1.DeleteResourceAction))
				if err != nil {
					return err
				}
				return nil
			}, timeout, pollingInterval).Should(BeNil())

			Byf("Verifying ServiceAccount kong/kong-serviceaccount is still on managed cluster")
			err = workloadClient.Get(context.TODO(),
				types.NamespacedName{Namespace: "kong", Name: "kong-serviceaccount"}, currentServiceAccount)
			Expect(err).To(BeNil())

			Byf("Verifying ServiceAccount cert-manager/cert-manager is still on managed cluster")
			err = workloadClient.Get(context.TODO(),
				types.NamespacedName{Namespace: certManager, Name: certManager}, currentServiceAccount)
			Expect(err).To(BeNil())

			Byf("Verifying Deployment mariadb/mariadb-mariadb-operator is still on managed cluster")
			err = workloadClient.Get(context.TODO(),
				types.NamespacedName{Namespace: "mariadb", Name: "mariadb-mariadb-operator"}, currentDepl)
			Expect(err).To(BeNil())

			Byf("Verifying Deployment cnpg-system/cnpg-cloudnative-pg is still on managed cluster")
			err = workloadClient.Get(context.TODO(),
				types.NamespacedName{Namespace: "cnpg-system", Name: "cnpg-cloudnative-pg"}, currentDepl)
			Expect(err).To(BeNil())

			Byf("Changing clusterSelector for ClusterProfile %s so to not match any cluster", dryRunClusterProfile.Name)
			Expect(k8sClient.Get(context.TODO(), types.NamespacedName{Name: dryRunClusterProfile.Name}, currentClusterProfile)).To(Succeed())

			currentClusterProfile.Spec.ClusterSelector = libsveltosv1beta1.Selector{
				LabelSelector: metav1.LabelSelector{
					MatchLabels: map[string]string{
						"bar": "foo",
					},
				},
			}
			Expect(k8sClient.Update(context.TODO(), currentClusterProfile)).To(Succeed())

			// Since ClusterProfile is in DryRun mode, ClusterSummary should be marked as deleted but not removed
			// In DryRun mode ClusterReport still needs to be updated.

			// First wait for clusterSummary to be marked for deletion
			Eventually(func() bool {
				currentClusterSummary := &configv1beta1.ClusterSummary{}
				err = k8sClient.Get(context.TODO(),
					types.NamespacedName{Namespace: dryRunClusterSummary.Namespace, Name: dryRunClusterSummary.Name}, currentClusterSummary)
				if err != nil {
					return false
				}
				return !currentClusterSummary.DeletionTimestamp.IsZero()
			}, timeout, pollingInterval).Should(BeTrue())

			// Then verify ClusterSummary is not removed.
			Consistently(func() bool {
				currentClusterSummary := &configv1beta1.ClusterSummary{}
				err = k8sClient.Get(context.TODO(),
					types.NamespacedName{Namespace: dryRunClusterSummary.Namespace, Name: dryRunClusterSummary.Name}, currentClusterSummary)
				if err != nil {
					return false
				}
				return !currentClusterSummary.DeletionTimestamp.IsZero()
			}, timeout/2, pollingInterval).Should(BeTrue())

			mariadDBRR = &libsveltosv1beta1.ResourceReport{
				Resource: libsveltosv1beta1.Resource{
					Kind: "ServiceAccount",
					Name: "mariadb-mariadb-operator",
				},
				Action: string(libsveltosv1beta1.DeleteResourceAction),
			}

			certManagerRR.Action = string(libsveltosv1beta1.DeleteResourceAction)

			cloudNativePGRR.Action = string(libsveltosv1beta1.DeleteResourceAction)

			By("Verifying ClusterReport")
			Eventually(func() error {
				currentClusterReport := &configv1beta1.ClusterReport{}
				err = k8sClient.Get(context.TODO(),
					types.NamespacedName{Namespace: dryRunClusterSummary.Spec.ClusterNamespace, Name: clusterReportName}, currentClusterReport)
				if err != nil {
					return err
				}
				if kindWorkloadCluster.GetKind() == libsveltosv1beta1.SveltosClusterKind {
					return verifyReleaseResources(currentClusterReport, mariadDBRR, certManagerRR, cloudNativePGRR)
				}
				// ClusterProfile is managing mysql release
				err = verifyReleaseReport(currentClusterReport, currentClusterProfile.Spec.HelmCharts[0].ReleaseNamespace,
					currentClusterProfile.Spec.HelmCharts[0].ReleaseName, string(configv1beta1.UninstallHelmAction))
				if err != nil {
					return err
				}
				// ClusterProfile is managing mysql release
				err = verifyReleaseReport(currentClusterReport, currentClusterProfile.Spec.HelmCharts[1].ReleaseNamespace,
					currentClusterProfile.Spec.HelmCharts[1].ReleaseName, string(configv1beta1.UninstallHelmAction))
				if err != nil {
					return err
				}
				// postgres is installed and action is Uninstall
				err = verifyReleaseReport(currentClusterReport, currentClusterProfile.Spec.HelmCharts[2].ReleaseNamespace,
					currentClusterProfile.Spec.HelmCharts[2].ReleaseName, string(configv1beta1.UninstallHelmAction))
				if err != nil {
					return err
				}
				return nil
			}, timeout, pollingInterval).Should(BeNil())

			By("Verifying ClusterReport for policy reports")
			Eventually(func() error {
				currentClusterReport := &configv1beta1.ClusterReport{}
				err = k8sClient.Get(context.TODO(),
					types.NamespacedName{Namespace: dryRunClusterSummary.Spec.ClusterNamespace, Name: clusterReportName}, currentClusterReport)
				if err != nil {
					return err
				}
				// If not in DryRun, it would create Kong Role
				err = verifyResourceReport(currentClusterReport, "kong2", "kong-leader-election",
					"Role", "rbac.authorization.k8s.io", string(libsveltosv1beta1.DeleteResourceAction))
				if err != nil {
					return err
				}
				// Previously installed this resource. Now not referencing the ConfigMap with this resource anymore.
				// So action would be delete
				err = verifyResourceReport(currentClusterReport, "kong", "kong-serviceaccount",
					"ServiceAccount", "", string(libsveltosv1beta1.DeleteResourceAction))
				if err != nil {
					return err
				}
				return nil
			}, timeout, pollingInterval).Should(BeNil())

			Byf("Changing syncMode to Continuous for ClusterProfile %s", dryRunClusterProfile.Name)
			Expect(k8sClient.Get(context.TODO(), types.NamespacedName{Name: dryRunClusterProfile.Name}, currentClusterProfile)).To(Succeed())
			currentClusterProfile.Spec.SyncMode = configv1beta1.SyncModeContinuous
			Expect(k8sClient.Update(context.TODO(), currentClusterProfile))

			verifyClusterSummary(clusterops.ClusterProfileLabelName,
				currentClusterProfile.Name, &currentClusterProfile.Spec,
				kindWorkloadCluster.GetNamespace(), kindWorkloadCluster.GetName(), getClusterType())

			Byf("Delete ClusterProfile %s", dryRunClusterProfile.Name)
			deleteClusterProfile(dryRunClusterProfile)

			Byf("Verifying ServiceAccount kong/kong-serviceaccount is removed from managed cluster")
			err = workloadClient.Get(context.TODO(),
				types.NamespacedName{Namespace: "kong", Name: "kong-serviceaccount"}, currentServiceAccount)
			Expect(err).ToNot(BeNil())
			Expect(apierrors.IsNotFound(err)).To(BeTrue())

			Byf("Verifying ServiceAccount cert-manager/cert-manager is removed from managed cluster")
			err = workloadClient.Get(context.TODO(),
				types.NamespacedName{Namespace: certManager, Name: certManager}, currentServiceAccount)
			Expect(err).ToNot(BeNil())
			Expect(apierrors.IsNotFound(err)).To(BeTrue())

			Byf("Verifying Deployment mariadb/mariadb-mariadb-operator is removed from managed cluster")
			err = workloadClient.Get(context.TODO(),
				types.NamespacedName{Namespace: "mariadb", Name: "mariadb-mariadb-operator"}, currentDepl)
			Expect(err).ToNot(BeNil())
			Expect(apierrors.IsNotFound(err)).To(BeTrue())

			Byf("Verifying Deployment cnpg-system/cnpg-cloudnative-pg is removed from managed cluster")
			err = workloadClient.Get(context.TODO(),
				types.NamespacedName{Namespace: "cnpg-system", Name: "cnpg-cloudnative-pg"}, currentDepl)
			Expect(err).ToNot(BeNil())
			Expect(apierrors.IsNotFound(err)).To(BeTrue())
		})
})

func verifyReleaseReport(clusterReport *configv1beta1.ClusterReport,
	releaseNamespace, releaseName, action string) error {

	for i := range clusterReport.Status.ReleaseReports {
		rr := clusterReport.Status.ReleaseReports[i]
		if rr.ReleaseName == releaseName && rr.ReleaseNamespace == releaseNamespace {
			if rr.Action == action {
				return nil
			}
			return fmt.Errorf("release %s/%s action %s does not match",
				releaseNamespace, releaseName, action)
		}
	}

	return fmt.Errorf("did not find entry for release %s/%s",
		releaseNamespace, releaseName)
}

func verifyResourceReport(clusterReport *configv1beta1.ClusterReport,
	resourceNamespace, resourceName, resourceKind, resourceGroup, action string) error {

	for i := range clusterReport.Status.ResourceReports {
		rr := clusterReport.Status.ResourceReports[i]
		if rr.Resource.Name == resourceName &&
			rr.Resource.Namespace == resourceNamespace &&
			rr.Resource.Kind == resourceKind &&
			rr.Resource.Group == resourceGroup {

			if rr.Action == action {
				return nil
			}
			return fmt.Errorf("resource %s (gropup %s) %s/%s action %s does not match",
				resourceKind, resourceGroup, resourceNamespace, resourceName, action)
		}
	}

	return fmt.Errorf("did not find entry for resource %s (gropup %s) %s/%s",
		resourceKind, resourceGroup, resourceNamespace, resourceName)
}

func verifyDeployedGroupVersionKind(clusterProfileName string) {
	Byf("Verifying DeployedGroupVersionKind are set for ClusterProfile %s", clusterProfileName)
	// Test has been flaky. Rarely it happens that Kong service is not removed
	// when clusterProfile is.
	// Adding this extra code to make test fails if at this points, ClusterSummary
	// has lost list of deployed GVKs (which will cause the cleanup to not happen)
	listOptions := []client.ListOption{
		client.MatchingLabels{
			clusterops.ClusterProfileLabelName: clusterProfileName,
		},
	}
	clusterSummaryList := &configv1beta1.ClusterSummaryList{}
	Expect(k8sClient.List(context.TODO(), clusterSummaryList, listOptions...)).To(Succeed())
	Expect(len(clusterSummaryList.Items)).To(Equal(1))
	found := false
	for i := range clusterSummaryList.Items[0].Status.DeployedGVKs {
		fs := clusterSummaryList.Items[0].Status.DeployedGVKs[i]
		if fs.FeatureID == libsveltosv1beta1.FeatureResources {
			Expect(len(fs.DeployedGroupVersionKind)).ToNot(BeZero())
			found = true
		}
	}
	Expect(found).To(BeTrue())
}

func setTier(clusterProfileName string, tier int32) {
	currentClusterProfile := &configv1beta1.ClusterProfile{}
	Expect(k8sClient.Get(context.TODO(),
		types.NamespacedName{Name: clusterProfileName},
		currentClusterProfile)).To(Succeed())
	currentClusterProfile.Spec.Tier = tier
	Expect(k8sClient.Update(context.TODO(), currentClusterProfile)).To(Succeed())
}

func verifyClusterReportForHelm(clusterReportName string, dryRunClusterSummary *configv1beta1.ClusterSummary,
	currentClusterProfile *configv1beta1.ClusterProfile) {

	if kindWorkloadCluster.GetKind() == libsveltosv1beta1.SveltosClusterKind {
		verifyClusterReportForHelmPullMode(clusterReportName, dryRunClusterSummary)
	} else {
		verifyClusterReportForHelmPushMode(clusterReportName, dryRunClusterSummary, currentClusterProfile)
	}
}

func verifyClusterReportForHelmPullMode(clusterReportName string, dryRunClusterSummary *configv1beta1.ClusterSummary) {
	Eventually(func() bool {
		currentClusterReport := &configv1beta1.ClusterReport{}
		err := k8sClient.Get(context.TODO(),
			types.NamespacedName{Namespace: dryRunClusterSummary.Spec.ClusterNamespace, Name: clusterReportName},
			currentClusterReport)
		if err != nil {
			return false
		}
		return currentClusterReport.Status.HelmResourceReports != nil
	}, timeout, pollingInterval).Should(BeTrue())
}

func verifyClusterReportForHelmPushMode(clusterReportName string, dryRunClusterSummary *configv1beta1.ClusterSummary,
	currentClusterProfile *configv1beta1.ClusterProfile) {

	Eventually(func() error {
		currentClusterReport := &configv1beta1.ClusterReport{}
		err := k8sClient.Get(context.TODO(),
			types.NamespacedName{Namespace: dryRunClusterSummary.Spec.ClusterNamespace, Name: clusterReportName},
			currentClusterReport)
		if err != nil {
			return err
		}
		// Another ClusterProfile is managing mysql release
		err = verifyReleaseReport(currentClusterReport, currentClusterProfile.Spec.HelmCharts[0].ReleaseNamespace,
			currentClusterProfile.Spec.HelmCharts[0].ReleaseName, string(configv1beta1.ConflictHelmAction))
		if err != nil {
			return err
		}
		// If not in DryRun, it would install redis release
		err = verifyReleaseReport(currentClusterReport, currentClusterProfile.Spec.HelmCharts[1].ReleaseNamespace,
			currentClusterProfile.Spec.HelmCharts[1].ReleaseName, string(configv1beta1.InstallHelmAction))
		if err != nil {
			return err
		}
		// postgres is Uninstall and not installed yet so no action
		err = verifyReleaseReport(currentClusterReport, currentClusterProfile.Spec.HelmCharts[2].ReleaseNamespace,
			currentClusterProfile.Spec.HelmCharts[2].ReleaseName, string(configv1beta1.NoHelmAction))
		if err != nil {
			return err
		}
		return nil
	}, timeout, pollingInterval).Should(BeNil())
}

func verifyReleaseResources(clusterReport *configv1beta1.ClusterReport,
	mariadDBRR, certManagerRR, cloudNativePGRR *libsveltosv1beta1.ResourceReport) error {

	if clusterReport.Status.HelmResourceReports == nil {
		return fmt.Errorf("helmResourceReports are empty")
	}

	// mariadb would be upgraded
	mariadbVerified := false
	for i := range clusterReport.Status.HelmResourceReports {
		rr := &clusterReport.Status.HelmResourceReports[i]
		if rr.Action == mariadDBRR.Action &&
			rr.Resource.Kind == mariadDBRR.Resource.Kind &&
			rr.Resource.Name == mariadDBRR.Resource.Name {

			mariadbVerified = true
		}
	}

	if !mariadbVerified {
		return fmt.Errorf("Verification failed for MariaDB")
	}

	// certmanager wont change
	certManagerVerified := false
	for i := range clusterReport.Status.HelmResourceReports {
		rr := &clusterReport.Status.HelmResourceReports[i]
		if rr.Action == certManagerRR.Action &&
			rr.Resource.Kind == certManagerRR.Resource.Kind &&
			rr.Resource.Name == certManagerRR.Resource.Name {

			certManagerVerified = true
		}
	}

	if !certManagerVerified {
		return fmt.Errorf("Verification failed for CertManager")
	}

	// cloudnative-pg would be removed
	cloudnativepgVerified := false
	for i := range clusterReport.Status.HelmResourceReports {
		rr := &clusterReport.Status.HelmResourceReports[i]
		if rr.Action == cloudNativePGRR.Action &&
			rr.Resource.Kind == cloudNativePGRR.Resource.Kind &&
			rr.Resource.Name == cloudNativePGRR.Resource.Name {

			cloudnativepgVerified = true
		}
	}

	if !cloudnativepgVerified {
		return fmt.Errorf("Verification failed for CloudNativePG")
	}

	return nil
}
