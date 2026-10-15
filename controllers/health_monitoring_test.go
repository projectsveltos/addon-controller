/*
Copyright 2026. projectsveltos.io. All rights reserved.

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

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"time"

	"github.com/go-logr/logr"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	lua "github.com/yuin/gopher-lua"

	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/events"
	"k8s.io/klog/v2/textlogger"
	"k8s.io/utils/ptr"
	clusterv1 "sigs.k8s.io/cluster-api/api/core/v1beta2"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/apiutil"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
	"sigs.k8s.io/controller-runtime/pkg/event"

	configv1beta1 "github.com/projectsveltos/addon-controller/api/v1beta1"
	"github.com/projectsveltos/addon-controller/controllers"
	libsveltosv1beta1 "github.com/projectsveltos/libsveltos/api/v1beta1"
	"github.com/projectsveltos/libsveltos/lib/deployer"
	fakedeployer "github.com/projectsveltos/libsveltos/lib/deployer/fake"
	sveltoslua "github.com/projectsveltos/libsveltos/lib/lua"
)

const (
	hmOwnerKind = "ClusterProfile"
	hmOwnerName = "platform-stack"

	hmNamespace       = "web-namespace"
	hmUserHealthCheck = "my-cert-expiry-check"

	hmHelmReleaseNameAnnotation      = "meta.helm.sh/release-name"
	hmHelmReleaseNamespaceAnnotation = "meta.helm.sh/release-namespace"

	hmStatusProgressing = "Progressing"

	hmCertManagerRelease = "cert-manager/cert-manager"
	hmGrafanaRelease     = "monitoring/grafana"
)

// healthResult is what the health monitoring Lua code reports for one resource
type healthResult struct {
	name    string
	status  string
	message string
}

// luaHealthOutput is the structure returned by the evaluate Lua function
type luaHealthOutput struct {
	Resources []struct {
		Resource struct {
			Metadata struct {
				Name string `json:"name"`
			} `json:"metadata"`
		} `json:"resource"`
		Status  string `json:"status"`
		Message string `json:"message"`
	} `json:"resources"`
}

// kindOf returns the Kubernetes kind of a typed object, e.g. Deployment for *appsv1.Deployment
func kindOf(obj interface{}) string {
	return reflect.TypeOf(obj).Elem().Name()
}

// toLuaResource converts a typed object to the table the HealthCheck Lua code receives. Resources reach
// the Lua code decoded from JSON, hence the round trip.
func toLuaResource(obj client.Object) lua.LValue {
	gvk, err := apiutil.GVKForObject(obj, scheme)
	Expect(err).To(BeNil())
	obj.GetObjectKind().SetGroupVersionKind(gvk)

	jsonResource, err := json.Marshal(obj)
	Expect(err).To(BeNil())
	decoded := map[string]interface{}{}
	Expect(json.Unmarshal(jsonResource, &decoded)).To(Succeed())

	return sveltoslua.MapToTable(decoded)
}

// evaluateHealthLua runs the health monitoring script against resources
func evaluateHealthLua(script string, resources ...client.Object) []healthResult {
	l := lua.NewState()
	defer l.Close()

	Expect(l.DoString(script)).To(Succeed())

	resourcesTable := l.NewTable()
	for i := range resources {
		resourcesTable.Append(toLuaResource(resources[i]))
	}
	l.SetGlobal("resources", resourcesTable)

	Expect(l.CallByParam(lua.P{Fn: l.GetGlobal("evaluate"), NRet: 1, Protect: true})).To(Succeed())

	tbl, ok := l.Get(-1).(*lua.LTable)
	Expect(ok).To(BeTrue())

	jsonOutput, err := json.Marshal(sveltoslua.ToGoValue(tbl))
	Expect(err).To(BeNil())
	var output luaHealthOutput
	Expect(json.Unmarshal(jsonOutput, &output)).To(Succeed())

	var results []healthResult
	for i := range output.Resources {
		results = append(results, healthResult{
			name:    output.Resources[i].Resource.Metadata.Name,
			status:  output.Resources[i].Status,
			message: output.Resources[i].Message,
		})
	}
	return results
}

func ownerAnnotations() map[string]string {
	return map[string]string{
		deployer.OwnerKind: hmOwnerKind,
		deployer.OwnerName: hmOwnerName,
	}
}

func helmAnnotations(namespace, release string) map[string]string {
	return map[string]string{
		hmHelmReleaseNameAnnotation:      release,
		hmHelmReleaseNamespaceAnnotation: namespace,
	}
}

func hmMetadata(name, uid string, annotations map[string]string, generation int64) metav1.ObjectMeta {
	return metav1.ObjectMeta{
		Name: name, Namespace: hmNamespace, UID: types.UID(uid), Generation: generation, Annotations: annotations,
	}
}

func hmDeployment(name, uid string, annotations map[string]string, generation int64,
	spec *appsv1.DeploymentSpec, status *appsv1.DeploymentStatus) *appsv1.Deployment {

	return &appsv1.Deployment{ObjectMeta: hmMetadata(name, uid, annotations, generation), Spec: *spec, Status: *status}
}

func hmStatefulSet(name, uid string, annotations map[string]string, generation int64,
	spec *appsv1.StatefulSetSpec, status *appsv1.StatefulSetStatus) *appsv1.StatefulSet {

	return &appsv1.StatefulSet{ObjectMeta: hmMetadata(name, uid, annotations, generation), Spec: *spec, Status: *status}
}

func hmDaemonSet(name, uid string, annotations map[string]string,
	spec *appsv1.DaemonSetSpec, status *appsv1.DaemonSetStatus) *appsv1.DaemonSet {

	return &appsv1.DaemonSet{ObjectMeta: hmMetadata(name, uid, annotations, 1), Spec: *spec, Status: *status}
}

func hmJob(name, uid string, conditions ...batchv1.JobCondition) *batchv1.Job {
	return &batchv1.Job{
		ObjectMeta: hmMetadata(name, uid, ownerAnnotations(), 1),
		Status:     batchv1.JobStatus{Conditions: conditions},
	}
}

func jobCondition(conditionType batchv1.JobConditionType, reason string) batchv1.JobCondition {
	return batchv1.JobCondition{Type: conditionType, Status: corev1.ConditionTrue, Reason: reason}
}

func rollingUpdateStrategy(partition int32) appsv1.StatefulSetUpdateStrategy {
	return appsv1.StatefulSetUpdateStrategy{
		Type:          appsv1.RollingUpdateStatefulSetStrategyType,
		RollingUpdate: &appsv1.RollingUpdateStatefulSetStrategy{Partition: ptr.To(partition)},
	}
}

var _ = Describe("Health monitoring: Lua health rules", func() {
	var script string

	BeforeEach(func() {
		script = controllers.RenderHealthMonitoringLua(hmOwnerKind, hmOwnerName,
			controllers.NewHelmReleases(hmCertManagerRelease, hmGrafanaRelease))
	})

	It("reports nothing for a Deployment that completed its rollout", func() {
		deployment := hmDeployment("web", "uid-1", ownerAnnotations(), 2,
			&appsv1.DeploymentSpec{Replicas: ptr.To[int32](3)},
			&appsv1.DeploymentStatus{ObservedGeneration: 2, Replicas: 3, UpdatedReplicas: 3, AvailableReplicas: 3})

		Expect(evaluateHealthLua(script, deployment)).To(BeEmpty())
	})

	It("reports a Deployment in the middle of a rollout as Progressing", func() {
		deployment := hmDeployment("web", "uid-1", ownerAnnotations(), 2,
			&appsv1.DeploymentSpec{Replicas: ptr.To[int32](3)},
			&appsv1.DeploymentStatus{ObservedGeneration: 2, Replicas: 4, UpdatedReplicas: 2, AvailableReplicas: 3})

		results := evaluateHealthLua(script, deployment)
		Expect(results).To(HaveLen(1))
		Expect(results[0].status).To(Equal("Progressing"))
		Expect(results[0].message).To(ContainSubstring("2 out of 3 new replicas have been updated"))
	})

	It("reports a Deployment whose new generation was not observed yet as Progressing", func() {
		deployment := hmDeployment("web", "uid-1", ownerAnnotations(), 3,
			&appsv1.DeploymentSpec{Replicas: ptr.To[int32](3)},
			&appsv1.DeploymentStatus{ObservedGeneration: 2, Replicas: 3, UpdatedReplicas: 3, AvailableReplicas: 3})

		results := evaluateHealthLua(script, deployment)
		Expect(results).To(HaveLen(1))
		Expect(results[0].status).To(Equal("Progressing"))
		Expect(results[0].message).To(ContainSubstring("observed deployment generation less than desired generation"))
	})

	It("reports a Deployment with updated replicas not available as Progressing", func() {
		deployment := hmDeployment("web", "uid-1", ownerAnnotations(), 2,
			&appsv1.DeploymentSpec{Replicas: ptr.To[int32](3)},
			&appsv1.DeploymentStatus{ObservedGeneration: 2, Replicas: 3, UpdatedReplicas: 3, AvailableReplicas: 1})

		results := evaluateHealthLua(script, deployment)
		Expect(results).To(HaveLen(1))
		Expect(results[0].status).To(Equal("Progressing"))
		Expect(results[0].message).To(ContainSubstring("1 of 3 updated replicas are available"))
	})

	It("reports a Deployment that exceeded its progress deadline as Degraded", func() {
		deployment := hmDeployment("web", "uid-1", ownerAnnotations(), 2,
			&appsv1.DeploymentSpec{Replicas: ptr.To[int32](3)},
			&appsv1.DeploymentStatus{
				ObservedGeneration: 2, Replicas: 3, UpdatedReplicas: 3,
				Conditions: []appsv1.DeploymentCondition{
					{
						Type: appsv1.DeploymentProgressing, Status: corev1.ConditionFalse,
						Reason: "ProgressDeadlineExceeded",
					},
				},
			})

		results := evaluateHealthLua(script, deployment)
		Expect(results).To(HaveLen(1))
		Expect(results[0].status).To(Equal("Degraded"))
	})

	It("reports a paused Deployment as Suspended", func() {
		deployment := hmDeployment("web", "uid-1", ownerAnnotations(), 2,
			&appsv1.DeploymentSpec{Replicas: ptr.To[int32](3), Paused: true},
			&appsv1.DeploymentStatus{ObservedGeneration: 2})

		results := evaluateHealthLua(script, deployment)
		Expect(results).To(HaveLen(1))
		Expect(results[0].status).To(Equal("Suspended"))
	})

	It("ignores resources not deployed by the profile", func() {
		broken := &appsv1.DeploymentSpec{Replicas: ptr.To[int32](3)}
		notObserved := &appsv1.DeploymentStatus{ObservedGeneration: 1}

		// Deployed by another profile
		otherProfile := ownerAnnotations()
		otherProfile[deployer.OwnerName] = "another-profile"
		// Same name, deployed by a Profile instead of a ClusterProfile
		otherKind := ownerAnnotations()
		otherKind[deployer.OwnerKind] = "Profile"

		results := evaluateHealthLua(script,
			hmDeployment("no-annotation", "uid-1", nil, 2, broken, notObserved),
			hmDeployment("other-profile", "uid-2", otherProfile, 2, broken, notObserved),
			hmDeployment("other-kind", "uid-3", otherKind, 2, broken, notObserved),
			// Helm release not deployed by this profile
			hmDeployment("other-release", "uid-4", helmAnnotations(testReleaseNameKyverno, testReleaseNameKyverno), 2,
				broken, notObserved),
			// Same release name in a different namespace
			hmDeployment("other-namespace", "uid-5", helmAnnotations(hmNamespace, "cert-manager"), 2, broken, notObserved),
		)

		Expect(results).To(BeEmpty())
	})

	It("evaluates resources belonging to a Helm release of the profile", func() {
		broken := &appsv1.DeploymentSpec{Replicas: ptr.To[int32](3)}
		notObserved := &appsv1.DeploymentStatus{ObservedGeneration: 1}

		results := evaluateHealthLua(script,
			hmDeployment("cert-manager", "uid-1", helmAnnotations("cert-manager", "cert-manager"), 2, broken, notObserved),
			hmDeployment("grafana", "uid-2", helmAnnotations("monitoring", "grafana"), 2, broken, notObserved),
		)

		Expect(results).To(HaveLen(2))
		Expect(results[0].name).To(Equal("cert-manager"))
		Expect(results[1].name).To(Equal("grafana"))
	})

	It("evaluates once a resource selected by more than one selector", func() {
		annotations := ownerAnnotations()
		for k, v := range helmAnnotations("cert-manager", "cert-manager") {
			annotations[k] = v
		}
		deployment := hmDeployment("web", "same-uid", annotations, 2,
			&appsv1.DeploymentSpec{Replicas: ptr.To[int32](3)}, &appsv1.DeploymentStatus{ObservedGeneration: 1})

		Expect(evaluateHealthLua(script, deployment, deployment)).To(HaveLen(1))
	})

	It("ignores kinds it has no rule for", func() {
		replicaSet := &appsv1.ReplicaSet{ObjectMeta: hmMetadata("rs", "uid-1", ownerAnnotations(), 1)}
		Expect(evaluateHealthLua(script, replicaSet)).To(BeEmpty())
	})

	It("evaluates StatefulSets", func() {
		healthy := hmStatefulSet("healthy", "uid-1", ownerAnnotations(), 1,
			&appsv1.StatefulSetSpec{Replicas: ptr.To[int32](3), UpdateStrategy: rollingUpdateStrategy(0)},
			&appsv1.StatefulSetStatus{ObservedGeneration: 1, ReadyReplicas: 3, UpdatedReplicas: 3})
		notReady := hmStatefulSet("not-ready", "uid-2", ownerAnnotations(), 1,
			&appsv1.StatefulSetSpec{Replicas: ptr.To[int32](3)},
			&appsv1.StatefulSetStatus{ObservedGeneration: 1, ReadyReplicas: 1})
		notObserved := hmStatefulSet("not-observed", "uid-3", ownerAnnotations(), 2,
			&appsv1.StatefulSetSpec{Replicas: ptr.To[int32](3)},
			&appsv1.StatefulSetStatus{ObservedGeneration: 1, ReadyReplicas: 3})
		rolling := hmStatefulSet("rolling", "uid-4", ownerAnnotations(), 1,
			&appsv1.StatefulSetSpec{Replicas: ptr.To[int32](3), UpdateStrategy: rollingUpdateStrategy(0)},
			&appsv1.StatefulSetStatus{ObservedGeneration: 1, ReadyReplicas: 3, UpdatedReplicas: 1})
		onDelete := hmStatefulSet("on-delete", "uid-5", ownerAnnotations(), 1,
			&appsv1.StatefulSetSpec{
				Replicas:       ptr.To[int32](3),
				UpdateStrategy: appsv1.StatefulSetUpdateStrategy{Type: appsv1.OnDeleteStatefulSetStrategyType},
			},
			&appsv1.StatefulSetStatus{ObservedGeneration: 1, ReadyReplicas: 3, UpdatedReplicas: 0})

		results := evaluateHealthLua(script, healthy, notReady, notObserved, rolling, onDelete)

		Expect(results).To(HaveLen(3))
		Expect(results[0].name).To(Equal("not-ready"))
		Expect(results[0].message).To(ContainSubstring("Waiting for 2 pods to be ready"))
		Expect(results[1].name).To(Equal("not-observed"))
		Expect(results[2].name).To(Equal("rolling"))
		Expect(results[2].message).To(ContainSubstring("1 out of 3 new pods have been updated"))
		for i := range results {
			Expect(results[i].status).To(Equal("Progressing"))
		}
	})

	It("evaluates DaemonSets", func() {
		healthy := hmDaemonSet("healthy", "uid-1", ownerAnnotations(), &appsv1.DaemonSetSpec{},
			&appsv1.DaemonSetStatus{
				ObservedGeneration: 1, DesiredNumberScheduled: 3, UpdatedNumberScheduled: 3, NumberAvailable: 3,
			})
		updating := hmDaemonSet("updating", "uid-2", ownerAnnotations(), &appsv1.DaemonSetSpec{},
			&appsv1.DaemonSetStatus{
				ObservedGeneration: 1, DesiredNumberScheduled: 3, UpdatedNumberScheduled: 1, NumberAvailable: 3,
			})
		unavailable := hmDaemonSet("unavailable", "uid-3", ownerAnnotations(), &appsv1.DaemonSetSpec{},
			&appsv1.DaemonSetStatus{
				ObservedGeneration: 1, DesiredNumberScheduled: 3, UpdatedNumberScheduled: 3, NumberAvailable: 2,
			})
		onDelete := hmDaemonSet("on-delete", "uid-4", ownerAnnotations(),
			&appsv1.DaemonSetSpec{UpdateStrategy: appsv1.DaemonSetUpdateStrategy{Type: appsv1.OnDeleteDaemonSetStrategyType}},
			&appsv1.DaemonSetStatus{ObservedGeneration: 1, DesiredNumberScheduled: 3})

		results := evaluateHealthLua(script, healthy, updating, unavailable, onDelete)

		Expect(results).To(HaveLen(2))
		Expect(results[0].name).To(Equal("updating"))
		Expect(results[0].message).To(ContainSubstring("1 out of 3 new pods have been updated"))
		Expect(results[1].name).To(Equal("unavailable"))
		Expect(results[1].message).To(ContainSubstring("2 of 3 updated pods are available"))
	})

	It("evaluates Jobs", func() {
		completed := hmJob("completed", "uid-1", jobCondition(batchv1.JobComplete, ""))
		running := hmJob("running", "uid-2")
		failed := hmJob("failed", "uid-3", jobCondition(batchv1.JobFailed, "BackoffLimitExceeded"))
		suspended := hmJob("suspended", "uid-4", jobCondition(batchv1.JobSuspended, ""))
		// A Failed condition wins over a Suspended one
		failedAndSuspended := hmJob("failed-and-suspended", "uid-5",
			jobCondition(batchv1.JobSuspended, ""), jobCondition(batchv1.JobFailed, "DeadlineExceeded"))
		// A condition that is not true does not count
		resumed := hmJob("resumed", "uid-6", batchv1.JobCondition{Type: batchv1.JobSuspended, Status: corev1.ConditionFalse})

		results := evaluateHealthLua(script, completed, running, failed, suspended, failedAndSuspended, resumed)

		Expect(results).To(HaveLen(5))
		Expect(results[0]).To(Equal(healthResult{name: "running", status: hmStatusProgressing, message: "Job has not completed yet"}))
		Expect(results[1].name).To(Equal("failed"))
		Expect(results[1].status).To(Equal("Degraded"))
		Expect(results[1].message).To(ContainSubstring("BackoffLimitExceeded"))
		Expect(results[2]).To(Equal(healthResult{name: "suspended", status: "Suspended", message: "Job is suspended"}))
		Expect(results[3].name).To(Equal("failed-and-suspended"))
		Expect(results[3].status).To(Equal("Degraded"))
		Expect(results[4].name).To(Equal("resumed"))
		Expect(results[4].status).To(Equal("Progressing"))
	})

	It("evaluates PersistentVolumeClaims", func() {
		pvc := func(name, uid string, phase corev1.PersistentVolumeClaimPhase) *corev1.PersistentVolumeClaim {
			return &corev1.PersistentVolumeClaim{
				ObjectMeta: hmMetadata(name, uid, ownerAnnotations(), 1),
				Status:     corev1.PersistentVolumeClaimStatus{Phase: phase},
			}
		}

		results := evaluateHealthLua(script,
			pvc("bound", "uid-1", corev1.ClaimBound),
			pvc("pending", "uid-2", corev1.ClaimPending),
			pvc("lost", "uid-3", corev1.ClaimLost),
			pvc("no-phase", "uid-4", ""),
		)

		Expect(results).To(HaveLen(3))
		Expect(results[0]).To(Equal(healthResult{name: "pending", status: hmStatusProgressing, message: "PersistentVolumeClaim is not bound yet"}))
		Expect(results[1].name).To(Equal("lost"))
		Expect(results[1].status).To(Equal("Degraded"))
		Expect(results[2].name).To(Equal("no-phase"))
		Expect(results[2].status).To(Equal("Progressing"))
	})

	It("quotes values placed in the Lua configuration", func() {
		Expect(controllers.QuoteLuaString(`plain`)).To(Equal(`"plain"`))
		Expect(controllers.QuoteLuaString(`a"b\c`)).To(Equal(`"a\"b\\c"`))
		Expect(controllers.QuoteLuaString("a\nb\rc")).To(Equal(`"a\nb\rc"`))
		Expect(controllers.QuoteLuaString("a\x01b")).To(Equal(`"a\001b"`))

		// A malicious value stays a string and cannot break out of the configuration
		malicious := `"}} os.exit(1) --`
		script := controllers.RenderHealthMonitoringLua(malicious, malicious,
			controllers.NewHelmReleases("ns/"+malicious))
		Expect(evaluateHealthLua(script)).To(BeEmpty())
	})
})

func newHealthMonitoringClusterSummary(clusterType libsveltosv1beta1.ClusterType) *configv1beta1.ClusterSummary {
	return &configv1beta1.ClusterSummary{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: randomString(),
			Name:      randomString(),
			UID:       types.UID(randomString()),
			OwnerReferences: []metav1.OwnerReference{
				{
					APIVersion: configv1beta1.GroupVersion.String(), Kind: configv1beta1.ClusterProfileKind,
					Name: hmOwnerName, UID: types.UID(randomString()),
				},
			},
		},
		Spec: configv1beta1.ClusterSummarySpec{
			ClusterNamespace: randomString(),
			ClusterName:      randomString(),
			ClusterType:      clusterType,
		},
	}
}

func provisionAllFeatures(clusterSummary *configv1beta1.ClusterSummary) {
	clusterSummary.Status.FeatureSummaries = []configv1beta1.FeatureSummary{
		{FeatureID: libsveltosv1beta1.FeatureHelm, Status: libsveltosv1beta1.FeatureStatusProvisioned},
		{FeatureID: libsveltosv1beta1.FeatureResources, Status: libsveltosv1beta1.FeatureStatusProvisioned},
	}
}

func addHelmReleases(clusterSummary *configv1beta1.ClusterSummary, namespaceAndNames ...string) {
	for _, namespaceAndName := range namespaceAndNames {
		parts := strings.SplitN(namespaceAndName, "/", 2)
		clusterSummary.Status.HelmReleaseSummaries = append(clusterSummary.Status.HelmReleaseSummaries,
			configv1beta1.HelmChartSummary{
				ReleaseNamespace: parts[0], ReleaseName: parts[1], Status: configv1beta1.HelmChartStatusManaging,
			})
	}
}

var _ = Describe("Health monitoring: resources generated for a ClusterSummary", func() {
	It("selects the resources deployed by Sveltos by label and the Helm ones by namespace", func() {
		selectors := controllers.GetMonitoringResourceSelectors(true,
			controllers.NewHelmReleases("monitoring/grafana", "cert-manager/cert-manager", "monitoring/loki"))

		// 5 kinds with the label, then 5 kinds in each of the two (sorted) namespaces
		type kindGroup struct{ kind, group string }
		kinds := []kindGroup{
			{kindOf(&appsv1.Deployment{}), appsv1.GroupName},
			{kindOf(&appsv1.StatefulSet{}), appsv1.GroupName},
			{kindOf(&appsv1.DaemonSet{}), appsv1.GroupName},
			{kindOf(&batchv1.Job{}), batchv1.GroupName},
			{kindOf(&corev1.PersistentVolumeClaim{}), corev1.GroupName},
		}
		Expect(selectors).To(HaveLen(len(kinds) * 3))

		for i, kg := range kinds {
			raw := selectors[i]
			Expect(raw.Kind).To(Equal(kg.kind))
			Expect(raw.Group).To(Equal(kg.group))
			Expect(raw.Namespace).To(BeEmpty())
			Expect(raw.LabelFilters).To(Equal([]libsveltosv1beta1.LabelFilter{
				{Key: "projectsveltos.io/reason", Operation: libsveltosv1beta1.OperationHas},
			}))

			for j, namespace := range []string{"cert-manager", "monitoring"} {
				helm := selectors[len(kinds)*(j+1)+i]
				Expect(helm.Kind).To(Equal(kg.kind))
				Expect(helm.Group).To(Equal(kg.group))
				Expect(helm.Namespace).To(Equal(namespace))
				Expect(helm.LabelFilters).To(BeEmpty())
			}
		}
		for i := range selectors {
			Expect(selectors[i].Version).To(Equal(appsv1.SchemeGroupVersion.Version))
		}

		Expect(controllers.GetMonitoringResourceSelectors(false, nil)).To(BeEmpty())
	})

	It("builds a HealthCheck for the resources and Helm releases of the ClusterSummary", func() {
		clusterSummary := newHealthMonitoringClusterSummary(libsveltosv1beta1.ClusterTypeSveltos)
		clusterSummary.Spec.ClusterProfileSpec.PolicyRefs = []configv1beta1.PolicyRef{
			{Kind: "ConfigMap", Namespace: hmNamespace, Name: "policies"},
		}
		addHelmReleases(clusterSummary, hmCertManagerRelease)
		// A release managed by another ClusterSummary must not be monitored
		clusterSummary.Status.HelmReleaseSummaries = append(clusterSummary.Status.HelmReleaseSummaries,
			configv1beta1.HelmChartSummary{
				ReleaseNamespace: testReleaseNameKyverno, ReleaseName: testReleaseNameKyverno, Status: configv1beta1.HelmChartStatusConflict,
			})

		healthCheck := controllers.BuildMonitoringHealthCheck(clusterSummary, hmOwnerKind, hmOwnerName)
		Expect(healthCheck).ToNot(BeNil())

		Expect(healthCheck.Name).To(Equal(controllers.GetHealthMonitoringName(clusterSummary)))
		Expect(healthCheck.Name).To(HavePrefix("hm-"))
		Expect(healthCheck.Labels).To(HaveKeyWithValue(
			configv1beta1.HealthMonitoringClusterSummaryUIDLabel, string(clusterSummary.UID)))
		Expect(healthCheck.Annotations).To(HaveKeyWithValue(
			configv1beta1.HealthMonitoringClusterSummaryNamespaceAnnotation, clusterSummary.Namespace))
		Expect(healthCheck.Annotations).To(HaveKeyWithValue(
			configv1beta1.HealthMonitoringClusterSummaryNameAnnotation, clusterSummary.Name))

		Expect(healthCheck.Spec.EvaluateIndependently).To(BeTrue())
		Expect(healthCheck.Spec.Flapping).ToNot(BeNil())
		Expect(healthCheck.Spec.Flapping.ConsecutiveEvaluations).To(Equal(int32(controllers.HealthMonitoringFlappingEvaluation)))
		// 5 kinds for resources, 5 kinds for the one Helm namespace
		Expect(healthCheck.Spec.ResourceSelectors).To(HaveLen(10))
		Expect(healthCheck.Spec.EvaluateHealth).To(ContainSubstring(`ownerName = "platform-stack"`))
		Expect(healthCheck.Spec.EvaluateHealth).To(ContainSubstring(`["cert-manager/cert-manager"] = true`))
		Expect(healthCheck.Spec.EvaluateHealth).ToNot(ContainSubstring(testReleaseNameKyverno))
	})

	It("builds no HealthCheck when the ClusterSummary deploys nothing to monitor", func() {
		clusterSummary := newHealthMonitoringClusterSummary(libsveltosv1beta1.ClusterTypeSveltos)
		Expect(controllers.BuildMonitoringHealthCheck(clusterSummary, hmOwnerKind, hmOwnerName)).To(BeNil())
	})

	It("builds a ClusterHealthCheck targeting only the cluster of the ClusterSummary", func() {
		clusterSummary := newHealthMonitoringClusterSummary(libsveltosv1beta1.ClusterTypeSveltos)

		clusterHealthCheck := controllers.BuildMonitoringClusterHealthCheck(clusterSummary, "hm-123",
			[]string{"zeta-check", "alpha-check"})

		Expect(clusterHealthCheck.Name).To(Equal(controllers.GetHealthMonitoringName(clusterSummary)))
		Expect(clusterHealthCheck.Spec.ClusterRefs).To(Equal([]corev1.ObjectReference{
			{
				APIVersion: libsveltosv1beta1.GroupVersion.String(), Kind: libsveltosv1beta1.SveltosClusterKind,
				Namespace: clusterSummary.Spec.ClusterNamespace, Name: clusterSummary.Spec.ClusterName,
			},
		}))
		// An empty selector matches no cluster: only ClusterRefs apply
		Expect(clusterHealthCheck.Spec.ClusterSelector.MatchLabels).To(BeEmpty())
		Expect(clusterHealthCheck.Spec.ClusterSelector.MatchExpressions).To(BeEmpty())
		// Required by the CRD: must be an empty list, not nil (nil is serialized as null)
		Expect(clusterHealthCheck.Spec.Notifications).ToNot(BeNil())
		Expect(clusterHealthCheck.Spec.Notifications).To(BeEmpty())

		Expect(clusterHealthCheck.Spec.LivenessChecks).To(HaveLen(3))
		Expect(clusterHealthCheck.Spec.LivenessChecks[0].Name).To(Equal(controllers.DefaultHealthCheckLivenessName))
		Expect(clusterHealthCheck.Spec.LivenessChecks[0].LivenessSourceRef.Name).To(Equal("hm-123"))
		Expect(clusterHealthCheck.Spec.LivenessChecks[1].LivenessSourceRef.Name).To(Equal("alpha-check"))
		Expect(clusterHealthCheck.Spec.LivenessChecks[2].LivenessSourceRef.Name).To(Equal("zeta-check"))
		for i := range clusterHealthCheck.Spec.LivenessChecks {
			Expect(clusterHealthCheck.Spec.LivenessChecks[i].Type).To(Equal(libsveltosv1beta1.LivenessTypeHealthCheck))
			Expect(clusterHealthCheck.Spec.LivenessChecks[i].LivenessSourceRef.Kind).To(Equal(libsveltosv1beta1.HealthCheckKind))
		}
	})

	It("targets a CAPI cluster with a CAPI cluster reference", func() {
		clusterSummary := newHealthMonitoringClusterSummary(libsveltosv1beta1.ClusterTypeCapi)

		clusterHealthCheck := controllers.BuildMonitoringClusterHealthCheck(clusterSummary, "hm-123", nil)

		Expect(clusterHealthCheck.Spec.ClusterRefs).To(Equal([]corev1.ObjectReference{
			{
				APIVersion: clusterv1.GroupVersion.String(), Kind: "Cluster",
				Namespace: clusterSummary.Spec.ClusterNamespace, Name: clusterSummary.Spec.ClusterName,
			},
		}))
	})
})

var _ = Describe("Health monitoring: reconciliation", func() {
	var (
		logger logr.Logger
		c      client.Client
		r      *controllers.ClusterSummaryReconciler
	)

	BeforeEach(func() {
		logger = textlogger.NewLogger(textlogger.NewConfig(textlogger.Verbosity(1)))
		c = fake.NewClientBuilder().WithScheme(scheme).Build()
		r = getClusterSummaryReconciler(c, fakedeployer.GetClient(context.TODO(), logger, c))
	})

	monitoredClusterSummary := func() *configv1beta1.ClusterSummary {
		clusterSummary := newHealthMonitoringClusterSummary(libsveltosv1beta1.ClusterTypeSveltos)
		clusterSummary.Spec.ClusterProfileSpec.HealthMonitoring = &configv1beta1.HealthMonitoring{Enabled: true}
		addHelmReleases(clusterSummary, hmCertManagerRelease)
		return clusterSummary
	}

	getHealthCheck := func(clusterSummary *configv1beta1.ClusterSummary) (*libsveltosv1beta1.HealthCheck, error) {
		healthCheck := &libsveltosv1beta1.HealthCheck{}
		err := c.Get(context.TODO(), types.NamespacedName{Name: controllers.GetHealthMonitoringName(clusterSummary)}, healthCheck)
		return healthCheck, err
	}

	getClusterHealthCheck := func(clusterSummary *configv1beta1.ClusterSummary) (*libsveltosv1beta1.ClusterHealthCheck, error) {
		clusterHealthCheck := &libsveltosv1beta1.ClusterHealthCheck{}
		err := c.Get(context.TODO(),
			types.NamespacedName{Name: controllers.GetHealthMonitoringName(clusterSummary)}, clusterHealthCheck)
		return clusterHealthCheck, err
	}

	It("creates nothing while features are not all provisioned", func() {
		clusterSummary := monitoredClusterSummary()
		clusterSummary.Status.FeatureSummaries = []configv1beta1.FeatureSummary{
			{FeatureID: libsveltosv1beta1.FeatureHelm, Status: libsveltosv1beta1.FeatureStatusProvisioned},
			{FeatureID: libsveltosv1beta1.FeatureResources, Status: libsveltosv1beta1.FeatureStatusProvisioning},
		}

		Expect(controllers.ReconcileHealthMonitoring(r, context.TODO(), clusterSummary, logger)).To(Succeed())

		_, err := getHealthCheck(clusterSummary)
		Expect(apierrors.IsNotFound(err)).To(BeTrue())
		_, err = getClusterHealthCheck(clusterSummary)
		Expect(apierrors.IsNotFound(err)).To(BeTrue())
	})

	It("creates the HealthCheck and the ClusterHealthCheck once all features are provisioned", func() {
		clusterSummary := monitoredClusterSummary()
		provisionAllFeatures(clusterSummary)

		Expect(controllers.ReconcileHealthMonitoring(r, context.TODO(), clusterSummary, logger)).To(Succeed())

		healthCheck, err := getHealthCheck(clusterSummary)
		Expect(err).To(BeNil())
		Expect(healthCheck.Spec.ResourceSelectors).To(HaveLen(5))

		clusterHealthCheck, err := getClusterHealthCheck(clusterSummary)
		Expect(err).To(BeNil())
		Expect(clusterHealthCheck.Spec.LivenessChecks).To(HaveLen(1))
		Expect(clusterHealthCheck.Spec.LivenessChecks[0].LivenessSourceRef.Name).To(Equal(healthCheck.Name))
	})

	It("keeps the resources, and updates the HealthCheck, when the deployed Helm releases change", func() {
		clusterSummary := monitoredClusterSummary()
		provisionAllFeatures(clusterSummary)
		Expect(controllers.ReconcileHealthMonitoring(r, context.TODO(), clusterSummary, logger)).To(Succeed())

		// The ClusterSummary is deploying again: nothing is touched
		clusterSummary.Status.FeatureSummaries[0].Status = libsveltosv1beta1.FeatureStatusProvisioning
		addHelmReleases(clusterSummary, testReleaseNameKyverno+"/"+testReleaseNameKyverno)
		Expect(controllers.ReconcileHealthMonitoring(r, context.TODO(), clusterSummary, logger)).To(Succeed())
		healthCheck, err := getHealthCheck(clusterSummary)
		Expect(err).To(BeNil())
		Expect(healthCheck.Spec.ResourceSelectors).To(HaveLen(5))

		// Provisioned again: the new release is monitored
		provisionAllFeatures(clusterSummary)
		Expect(controllers.ReconcileHealthMonitoring(r, context.TODO(), clusterSummary, logger)).To(Succeed())
		healthCheck, err = getHealthCheck(clusterSummary)
		Expect(err).To(BeNil())
		Expect(healthCheck.Spec.ResourceSelectors).To(HaveLen(10))
		Expect(healthCheck.Spec.EvaluateHealth).To(ContainSubstring(`["` + testReleaseNameKyverno + `/` + testReleaseNameKyverno + `"] = true`))
	})

	It("monitors only the listed HealthChecks when defaultChecks is false", func() {
		clusterSummary := monitoredClusterSummary()
		clusterSummary.Spec.ClusterProfileSpec.HealthMonitoring.DefaultChecks = ptr.To(false)
		clusterSummary.Spec.ClusterProfileSpec.HealthMonitoring.HealthChecks = []string{hmUserHealthCheck}
		provisionAllFeatures(clusterSummary)

		Expect(controllers.ReconcileHealthMonitoring(r, context.TODO(), clusterSummary, logger)).To(Succeed())

		_, err := getHealthCheck(clusterSummary)
		Expect(apierrors.IsNotFound(err)).To(BeTrue())

		clusterHealthCheck, err := getClusterHealthCheck(clusterSummary)
		Expect(err).To(BeNil())
		Expect(clusterHealthCheck.Spec.LivenessChecks).To(HaveLen(1))
		Expect(clusterHealthCheck.Spec.LivenessChecks[0].LivenessSourceRef.Name).To(Equal(hmUserHealthCheck))
	})

	It("monitors the default and the listed HealthChecks, and removes the generated one when not needed anymore", func() {
		clusterSummary := monitoredClusterSummary()
		clusterSummary.Spec.ClusterProfileSpec.HealthMonitoring.HealthChecks = []string{hmUserHealthCheck}
		provisionAllFeatures(clusterSummary)
		Expect(controllers.ReconcileHealthMonitoring(r, context.TODO(), clusterSummary, logger)).To(Succeed())

		clusterHealthCheck, err := getClusterHealthCheck(clusterSummary)
		Expect(err).To(BeNil())
		Expect(clusterHealthCheck.Spec.LivenessChecks).To(HaveLen(2))
		_, err = getHealthCheck(clusterSummary)
		Expect(err).To(BeNil())

		clusterSummary.Spec.ClusterProfileSpec.HealthMonitoring.DefaultChecks = ptr.To(false)
		Expect(controllers.ReconcileHealthMonitoring(r, context.TODO(), clusterSummary, logger)).To(Succeed())

		clusterHealthCheck, err = getClusterHealthCheck(clusterSummary)
		Expect(err).To(BeNil())
		Expect(clusterHealthCheck.Spec.LivenessChecks).To(HaveLen(1))
		_, err = getHealthCheck(clusterSummary)
		Expect(apierrors.IsNotFound(err)).To(BeTrue())
	})

	It("creates nothing when there is nothing to monitor", func() {
		clusterSummary := newHealthMonitoringClusterSummary(libsveltosv1beta1.ClusterTypeSveltos)
		clusterSummary.Spec.ClusterProfileSpec.HealthMonitoring = &configv1beta1.HealthMonitoring{Enabled: true}
		provisionAllFeatures(clusterSummary)

		Expect(controllers.ReconcileHealthMonitoring(r, context.TODO(), clusterSummary, logger)).To(Succeed())

		_, err := getClusterHealthCheck(clusterSummary)
		Expect(apierrors.IsNotFound(err)).To(BeTrue())
	})

	It("removes the resources when monitoring is disabled", func() {
		clusterSummary := monitoredClusterSummary()
		provisionAllFeatures(clusterSummary)
		Expect(controllers.ReconcileHealthMonitoring(r, context.TODO(), clusterSummary, logger)).To(Succeed())

		// A HealthCheck written by the user must not be touched
		userHealthCheck := &libsveltosv1beta1.HealthCheck{ObjectMeta: metav1.ObjectMeta{Name: hmUserHealthCheck}}
		Expect(c.Create(context.TODO(), userHealthCheck)).To(Succeed())

		clusterSummary.Spec.ClusterProfileSpec.HealthMonitoring.Enabled = false
		Expect(controllers.ReconcileHealthMonitoring(r, context.TODO(), clusterSummary, logger)).To(Succeed())

		_, err := getHealthCheck(clusterSummary)
		Expect(apierrors.IsNotFound(err)).To(BeTrue())
		_, err = getClusterHealthCheck(clusterSummary)
		Expect(apierrors.IsNotFound(err)).To(BeTrue())
		Expect(c.Get(context.TODO(), client.ObjectKeyFromObject(userHealthCheck), userHealthCheck)).To(Succeed())
	})

	It("removeHealthMonitoring deletes only what was created for the ClusterSummary", func() {
		clusterSummary := monitoredClusterSummary()
		provisionAllFeatures(clusterSummary)
		Expect(controllers.ReconcileHealthMonitoring(r, context.TODO(), clusterSummary, logger)).To(Succeed())

		// Same name, but created for another ClusterSummary (different UID label)
		other := clusterSummary.DeepCopy()
		other.UID = types.UID(randomString())
		Expect(controllers.RemoveHealthMonitoring(r, context.TODO(), other, logger)).To(Succeed())
		_, err := getHealthCheck(clusterSummary)
		Expect(err).To(BeNil())
		_, err = getClusterHealthCheck(clusterSummary)
		Expect(err).To(BeNil())

		Expect(controllers.RemoveHealthMonitoring(r, context.TODO(), clusterSummary, logger)).To(Succeed())
		_, err = getHealthCheck(clusterSummary)
		Expect(apierrors.IsNotFound(err)).To(BeTrue())
		_, err = getClusterHealthCheck(clusterSummary)
		Expect(apierrors.IsNotFound(err)).To(BeTrue())

		// Nothing left to remove is not an error
		Expect(controllers.RemoveHealthMonitoring(r, context.TODO(), clusterSummary, logger)).To(Succeed())
	})

	It("does nothing for a DryRun ClusterSummary", func() {
		clusterSummary := monitoredClusterSummary()
		clusterSummary.Spec.ClusterProfileSpec.SyncMode = configv1beta1.SyncModeDryRun
		provisionAllFeatures(clusterSummary)

		Expect(controllers.ReconcileHealthMonitoring(r, context.TODO(), clusterSummary, logger)).To(Succeed())

		_, err := getClusterHealthCheck(clusterSummary)
		Expect(apierrors.IsNotFound(err)).To(BeTrue())
	})
})

var _ = Describe("Health monitoring: HealthChecks of a Profile", func() {
	var (
		logger   logr.Logger
		c        client.Client
		r        *controllers.ClusterSummaryReconciler
		recorder *events.FakeRecorder
	)

	BeforeEach(func() {
		logger = textlogger.NewLogger(textlogger.NewConfig(textlogger.Verbosity(1)))
		c = fake.NewClientBuilder().WithScheme(scheme).Build()
		r = getClusterSummaryReconciler(c, fakedeployer.GetClient(context.TODO(), logger, c))
		recorder = events.NewFakeRecorder(10)
		controllers.SetEventRecorder(r, recorder)
	})

	// newProvisionedClusterSummary returns a provisioned ClusterSummary, owned by a profile of kind
	// profileKind, listing a HealthCheck
	newProvisionedClusterSummary := func(profileKind string) *configv1beta1.ClusterSummary {
		clusterSummary := newHealthMonitoringClusterSummary(libsveltosv1beta1.ClusterTypeSveltos)
		clusterSummary.OwnerReferences[0].Kind = profileKind
		clusterSummary.Spec.ClusterProfileSpec.HealthMonitoring = &configv1beta1.HealthMonitoring{
			Enabled: true, HealthChecks: []string{hmUserHealthCheck},
		}
		addHelmReleases(clusterSummary, hmCertManagerRelease)
		provisionAllFeatures(clusterSummary)
		return clusterSummary
	}

	getClusterHealthCheck := func(clusterSummary *configv1beta1.ClusterSummary) (*libsveltosv1beta1.ClusterHealthCheck, error) {
		clusterHealthCheck := &libsveltosv1beta1.ClusterHealthCheck{}
		err := c.Get(context.TODO(),
			types.NamespacedName{Name: controllers.GetHealthMonitoringName(clusterSummary)}, clusterHealthCheck)
		return clusterHealthCheck, err
	}

	It("a Profile gets the default checks only, and a warning explains why the HealthChecks are ignored", func() {
		clusterSummary := newProvisionedClusterSummary(configv1beta1.ProfileKind)

		Expect(controllers.ReconcileHealthMonitoring(r, context.TODO(), clusterSummary, logger)).To(Succeed())

		clusterHealthCheck, err := getClusterHealthCheck(clusterSummary)
		Expect(err).To(BeNil())
		Expect(clusterHealthCheck.Spec.LivenessChecks).To(HaveLen(1))
		Expect(clusterHealthCheck.Spec.LivenessChecks[0].Name).To(Equal(controllers.DefaultHealthCheckLivenessName))
		Expect(clusterHealthCheck.Spec.LivenessChecks[0].LivenessSourceRef.Name).ToNot(Equal(hmUserHealthCheck))

		Expect(recorder.Events).To(Receive(And(
			ContainSubstring("Warning HealthChecksIgnored"), ContainSubstring("only a ClusterProfile"))))
	})

	It("a Profile listing HealthChecks, with defaultChecks false, monitors nothing", func() {
		clusterSummary := newProvisionedClusterSummary(configv1beta1.ProfileKind)
		clusterSummary.Spec.ClusterProfileSpec.HealthMonitoring.DefaultChecks = ptr.To(false)

		Expect(controllers.ReconcileHealthMonitoring(r, context.TODO(), clusterSummary, logger)).To(Succeed())

		_, err := getClusterHealthCheck(clusterSummary)
		Expect(apierrors.IsNotFound(err)).To(BeTrue())
		Expect(recorder.Events).To(Receive(ContainSubstring("Warning HealthChecksIgnored")))
	})

	It("a Profile listing no HealthChecks gets no warning", func() {
		clusterSummary := newProvisionedClusterSummary(configv1beta1.ProfileKind)
		clusterSummary.Spec.ClusterProfileSpec.HealthMonitoring.HealthChecks = nil

		Expect(controllers.ReconcileHealthMonitoring(r, context.TODO(), clusterSummary, logger)).To(Succeed())

		_, err := getClusterHealthCheck(clusterSummary)
		Expect(err).To(BeNil())
		Expect(recorder.Events).ToNot(Receive())
	})

	It("a ClusterProfile keeps its HealthChecks and gets no warning", func() {
		clusterSummary := newProvisionedClusterSummary(configv1beta1.ClusterProfileKind)

		Expect(controllers.ReconcileHealthMonitoring(r, context.TODO(), clusterSummary, logger)).To(Succeed())

		clusterHealthCheck, err := getClusterHealthCheck(clusterSummary)
		Expect(err).To(BeNil())
		Expect(clusterHealthCheck.Spec.LivenessChecks).To(HaveLen(2))
		Expect(recorder.Events).ToNot(Receive())
	})
})

var _ = Describe("Health monitoring: ownership and failures", func() {
	var (
		logger logr.Logger
		c      client.Client
		r      *controllers.ClusterSummaryReconciler
	)

	BeforeEach(func() {
		logger = textlogger.NewLogger(textlogger.NewConfig(textlogger.Verbosity(1)))
	})

	newReconciler := func(funcs interceptor.Funcs) {
		c = fake.NewClientBuilder().WithScheme(scheme).WithInterceptorFuncs(funcs).Build()
		r = getClusterSummaryReconciler(c, fakedeployer.GetClient(context.TODO(), logger, c))
	}

	newProvisionedClusterSummary := func() *configv1beta1.ClusterSummary {
		clusterSummary := newHealthMonitoringClusterSummary(libsveltosv1beta1.ClusterTypeSveltos)
		clusterSummary.Spec.ClusterProfileSpec.HealthMonitoring = &configv1beta1.HealthMonitoring{Enabled: true}
		addHelmReleases(clusterSummary, hmCertManagerRelease)
		provisionAllFeatures(clusterSummary)
		return clusterSummary
	}

	nameOf := func(clusterSummary *configv1beta1.ClusterSummary) types.NamespacedName {
		return types.NamespacedName{Name: controllers.GetHealthMonitoringName(clusterSummary)}
	}

	It("never deletes an object with the generated name which another ClusterSummary created", func() {
		newReconciler(interceptor.Funcs{})
		clusterSummary := newProvisionedClusterSummary()

		// No label at all, and the label of another ClusterSummary
		for _, labels := range []map[string]string{
			nil,
			{configv1beta1.HealthMonitoringClusterSummaryUIDLabel: randomString()},
		} {
			healthCheck := &libsveltosv1beta1.HealthCheck{
				ObjectMeta: metav1.ObjectMeta{Name: nameOf(clusterSummary).Name, Labels: labels}}
			clusterHealthCheck := &libsveltosv1beta1.ClusterHealthCheck{
				ObjectMeta: metav1.ObjectMeta{Name: nameOf(clusterSummary).Name, Labels: labels}}
			Expect(c.Create(context.TODO(), healthCheck)).To(Succeed())
			Expect(c.Create(context.TODO(), clusterHealthCheck)).To(Succeed())

			Expect(controllers.RemoveHealthMonitoring(r, context.TODO(), clusterSummary, logger)).To(Succeed())

			Expect(c.Get(context.TODO(), nameOf(clusterSummary), &libsveltosv1beta1.HealthCheck{})).To(Succeed())
			Expect(c.Get(context.TODO(), nameOf(clusterSummary), &libsveltosv1beta1.ClusterHealthCheck{})).To(Succeed())

			Expect(c.Delete(context.TODO(), healthCheck)).To(Succeed())
			Expect(c.Delete(context.TODO(), clusterHealthCheck)).To(Succeed())
		}
	})

	It("does not touch the objects of another ClusterSummary when monitoring is disabled", func() {
		newReconciler(interceptor.Funcs{})
		clusterSummary := newProvisionedClusterSummary()
		clusterHealthCheck := &libsveltosv1beta1.ClusterHealthCheck{ObjectMeta: metav1.ObjectMeta{
			Name:   nameOf(clusterSummary).Name,
			Labels: map[string]string{configv1beta1.HealthMonitoringClusterSummaryUIDLabel: randomString()},
		}}
		Expect(c.Create(context.TODO(), clusterHealthCheck)).To(Succeed())

		clusterSummary.Spec.ClusterProfileSpec.HealthMonitoring.Enabled = false
		Expect(controllers.ReconcileHealthMonitoring(r, context.TODO(), clusterSummary, logger)).To(Succeed())

		Expect(c.Get(context.TODO(), nameOf(clusterSummary), &libsveltosv1beta1.ClusterHealthCheck{})).To(Succeed())
	})

	It("creates nothing for a ClusterSummary being deleted", func() {
		newReconciler(interceptor.Funcs{})
		clusterSummary := newProvisionedClusterSummary()
		clusterSummary.DeletionTimestamp = &metav1.Time{Time: time.Now()}

		Expect(controllers.ReconcileHealthMonitoring(r, context.TODO(), clusterSummary, logger)).To(Succeed())

		err := c.Get(context.TODO(), nameOf(clusterSummary), &libsveltosv1beta1.ClusterHealthCheck{})
		Expect(apierrors.IsNotFound(err)).To(BeTrue())
	})

	It("creates nothing for a ClusterSummary with no feature summaries", func() {
		newReconciler(interceptor.Funcs{})
		clusterSummary := newProvisionedClusterSummary()
		clusterSummary.Status.FeatureSummaries = nil

		Expect(controllers.ReconcileHealthMonitoring(r, context.TODO(), clusterSummary, logger)).To(Succeed())

		err := c.Get(context.TODO(), nameOf(clusterSummary), &libsveltosv1beta1.ClusterHealthCheck{})
		Expect(apierrors.IsNotFound(err)).To(BeTrue())
	})

	It("fails, creating nothing, when the profile owning the ClusterSummary cannot be found", func() {
		newReconciler(interceptor.Funcs{})
		clusterSummary := newProvisionedClusterSummary()
		clusterSummary.OwnerReferences = nil

		Expect(controllers.ReconcileHealthMonitoring(r, context.TODO(), clusterSummary, logger)).ToNot(Succeed())

		err := c.Get(context.TODO(), nameOf(clusterSummary), &libsveltosv1beta1.ClusterHealthCheck{})
		Expect(apierrors.IsNotFound(err)).To(BeTrue())
	})

	It("builds the same HealthCheck whatever the order of the Helm releases", func() {
		first := newProvisionedClusterSummary()
		first.Status.HelmReleaseSummaries = nil
		addHelmReleases(first, hmCertManagerRelease, hmGrafanaRelease, "monitoring/loki")

		second := first.DeepCopy()
		second.Status.HelmReleaseSummaries = nil
		addHelmReleases(second, "monitoring/loki", hmGrafanaRelease, hmCertManagerRelease)

		firstHealthCheck := controllers.BuildMonitoringHealthCheck(first, hmOwnerKind, hmOwnerName)
		secondHealthCheck := controllers.BuildMonitoringHealthCheck(second, hmOwnerKind, hmOwnerName)
		Expect(firstHealthCheck.Spec).To(Equal(secondHealthCheck.Spec))
	})

	It("removing is not an error when the HealthCheck CRDs are not installed", func() {
		newReconciler(interceptor.Funcs{
			Get: func(_ context.Context, _ client.WithWatch, _ client.ObjectKey, _ client.Object,
				_ ...client.GetOption) error {

				return &meta.NoKindMatchError{}
			},
		})

		Expect(controllers.RemoveHealthMonitoring(r, context.TODO(), newProvisionedClusterSummary(), logger)).To(Succeed())
	})

	It("removing fails when the object cannot be read", func() {
		newReconciler(interceptor.Funcs{
			Get: func(_ context.Context, _ client.WithWatch, _ client.ObjectKey, _ client.Object,
				_ ...client.GetOption) error {

				return errors.New("read failure")
			},
		})

		Expect(controllers.RemoveHealthMonitoring(r, context.TODO(), newProvisionedClusterSummary(), logger)).ToNot(Succeed())
	})

	It("removing fails when an object cannot be deleted, and ignores one deleted in the meantime", func() {
		deleteError := errors.New("delete failure")
		newReconciler(interceptor.Funcs{
			Delete: func(_ context.Context, _ client.WithWatch, _ client.Object, _ ...client.DeleteOption) error {
				return deleteError
			},
		})
		clusterSummary := newProvisionedClusterSummary()
		Expect(controllers.ReconcileHealthMonitoring(r, context.TODO(), clusterSummary, logger)).To(Succeed())

		Expect(controllers.RemoveHealthMonitoring(r, context.TODO(), clusterSummary, logger)).ToNot(Succeed())

		deleteError = apierrors.NewNotFound(schema.GroupResource{}, "deleted already")
		Expect(controllers.RemoveHealthMonitoring(r, context.TODO(), clusterSummary, logger)).To(Succeed())
	})
})

var _ = Describe("Health monitoring: validation of healthMonitoring", func() {
	// createClusterProfile creates a ClusterProfile in the API server, so the validation rules of the CRD apply
	createClusterProfile := func(healthMonitoring *configv1beta1.HealthMonitoring) error {
		clusterProfile := &configv1beta1.ClusterProfile{
			ObjectMeta: metav1.ObjectMeta{Name: randomString()},
			Spec:       configv1beta1.Spec{HealthMonitoring: healthMonitoring},
		}
		err := testEnv.Create(context.TODO(), clusterProfile)
		if err == nil {
			DeferCleanup(func() { Expect(testEnv.Delete(context.TODO(), clusterProfile)).To(Succeed()) })
		}
		return err
	}

	It("accepts valid combinations", func() {
		Expect(createClusterProfile(nil)).To(Succeed())
		Expect(createClusterProfile(&configv1beta1.HealthMonitoring{Enabled: false})).To(Succeed())
		Expect(createClusterProfile(&configv1beta1.HealthMonitoring{Enabled: true})).To(Succeed())
		Expect(createClusterProfile(&configv1beta1.HealthMonitoring{
			Enabled: true, HealthChecks: []string{hmUserHealthCheck}})).To(Succeed())
		Expect(createClusterProfile(&configv1beta1.HealthMonitoring{
			Enabled: true, DefaultChecks: ptr.To(false), HealthChecks: []string{hmUserHealthCheck}})).To(Succeed())
	})

	It("rejects healthChecks when monitoring is not enabled", func() {
		err := createClusterProfile(&configv1beta1.HealthMonitoring{
			Enabled: false, HealthChecks: []string{hmUserHealthCheck}})
		Expect(err).ToNot(BeNil())
		Expect(err.Error()).To(ContainSubstring("healthChecks can only be set when enabled is true"))
	})

	It("rejects defaultChecks false without healthChecks, as nothing would be monitored", func() {
		err := createClusterProfile(&configv1beta1.HealthMonitoring{Enabled: true, DefaultChecks: ptr.To(false)})
		Expect(err).ToNot(BeNil())
		Expect(err.Error()).To(ContainSubstring("defaultChecks cannot be false when healthChecks is empty"))
	})
})

var _ = Describe("Health monitoring: ClusterSummary predicate", func() {
	var predicate controllers.ClusterSummaryPredicate

	BeforeEach(func() {
		predicate = controllers.ClusterSummaryPredicate{
			Logger: textlogger.NewLogger(textlogger.NewConfig(textlogger.Verbosity(1))),
		}
	})

	It("reconciles when all features become provisioned and health monitoring is enabled", func() {
		oldClusterSummary := newHealthMonitoringClusterSummary(libsveltosv1beta1.ClusterTypeSveltos)
		oldClusterSummary.Spec.ClusterProfileSpec.HealthMonitoring = &configv1beta1.HealthMonitoring{Enabled: true}
		oldClusterSummary.Status.FeatureSummaries = []configv1beta1.FeatureSummary{
			{FeatureID: libsveltosv1beta1.FeatureHelm, Status: libsveltosv1beta1.FeatureStatusProvisioning},
		}

		newClusterSummary := oldClusterSummary.DeepCopy()
		newClusterSummary.Status.FeatureSummaries[0].Status = libsveltosv1beta1.FeatureStatusProvisioned

		Expect(predicate.Update(event.UpdateEvent{ObjectOld: oldClusterSummary, ObjectNew: newClusterSummary})).To(BeTrue())
	})

	It("reconciles when the Helm releases managed by the ClusterSummary change", func() {
		oldClusterSummary := newHealthMonitoringClusterSummary(libsveltosv1beta1.ClusterTypeSveltos)
		oldClusterSummary.Spec.ClusterProfileSpec.HealthMonitoring = &configv1beta1.HealthMonitoring{Enabled: true}
		provisionAllFeatures(oldClusterSummary)
		addHelmReleases(oldClusterSummary, hmCertManagerRelease)

		newClusterSummary := oldClusterSummary.DeepCopy()
		addHelmReleases(newClusterSummary, testReleaseNameKyverno+"/"+testReleaseNameKyverno)

		Expect(predicate.Update(event.UpdateEvent{ObjectOld: oldClusterSummary, ObjectNew: newClusterSummary})).To(BeTrue())
	})

	It("does not reconcile for a status change that does not matter to health monitoring", func() {
		oldClusterSummary := newHealthMonitoringClusterSummary(libsveltosv1beta1.ClusterTypeSveltos)
		oldClusterSummary.Spec.ClusterProfileSpec.HealthMonitoring = &configv1beta1.HealthMonitoring{Enabled: true}
		provisionAllFeatures(oldClusterSummary)

		newClusterSummary := oldClusterSummary.DeepCopy()
		message := "something"
		newClusterSummary.Status.Dependencies = &message

		Expect(predicate.Update(event.UpdateEvent{ObjectOld: oldClusterSummary, ObjectNew: newClusterSummary})).To(BeFalse())
	})

	It("keeps ignoring features becoming provisioned when health monitoring is not enabled", func() {
		oldClusterSummary := newHealthMonitoringClusterSummary(libsveltosv1beta1.ClusterTypeSveltos)
		oldClusterSummary.Status.FeatureSummaries = []configv1beta1.FeatureSummary{
			{FeatureID: libsveltosv1beta1.FeatureHelm, Status: libsveltosv1beta1.FeatureStatusProvisioning},
		}

		newClusterSummary := oldClusterSummary.DeepCopy()
		newClusterSummary.Status.FeatureSummaries[0].Status = libsveltosv1beta1.FeatureStatusProvisioned

		Expect(predicate.Update(event.UpdateEvent{ObjectOld: oldClusterSummary, ObjectNew: newClusterSummary})).To(BeFalse())
	})
})
