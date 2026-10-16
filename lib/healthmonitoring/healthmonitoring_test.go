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

package healthmonitoring_test

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	configv1beta1 "github.com/projectsveltos/addon-controller/api/v1beta1"
	"github.com/projectsveltos/addon-controller/lib/healthmonitoring"
	libsveltosv1beta1 "github.com/projectsveltos/libsveltos/api/v1beta1"
)

const (
	testNamespace      = "ns"
	generatedCheckName = "hm-1"
	listedCheck        = "mine"
	testUID            = "uid"
	summaryName        = "summary"
	resourceNamespace  = "d"
)

func summaryInput(name, profileName string, enabled bool,
	generatedCheck string, listedChecks ...string) healthmonitoring.SummaryInput {

	return healthmonitoring.SummaryInput{
		Summary:        corev1.ObjectReference{Kind: configv1beta1.ClusterSummaryKind, Namespace: testNamespace, Name: name},
		Profile:        corev1.ObjectReference{Kind: configv1beta1.ClusterProfileKind, Name: profileName},
		Enabled:        enabled,
		GeneratedCheck: generatedCheck,
		ListedChecks:   listedChecks,
	}
}

func resourceInput(kind, name string, status libsveltosv1beta1.HealthStatus,
	message string) healthmonitoring.ResourceInput {

	return healthmonitoring.ResourceInput{
		ObjectRef: corev1.ObjectReference{APIVersion: "apps/v1", Kind: kind, Namespace: resourceNamespace, Name: name},
		Status:    status,
		Message:   message,
	}
}

var _ = Describe("Evaluate", func() {
	It("returns NotEnabled when no summary has monitoring enabled", func() {
		summaries := []healthmonitoring.SummaryInput{
			summaryInput("s1", "p1", false, generatedCheckName),
		}
		reports := []healthmonitoring.ReportInput{{HealthCheckName: generatedCheckName}}

		result := healthmonitoring.Evaluate(summaries, reports)
		Expect(result.State).To(Equal(healthmonitoring.StateNotEnabled))
		Expect(result.Profiles).To(BeEmpty())
		Expect(result.Resources).To(BeEmpty())

		Expect(healthmonitoring.Evaluate(nil, nil).State).To(Equal(healthmonitoring.StateNotEnabled))
	})

	It("returns NoData for a profile and the cluster when no report exists", func() {
		summaries := []healthmonitoring.SummaryInput{
			summaryInput("s1", "p1", true, generatedCheckName),
		}

		result := healthmonitoring.Evaluate(summaries, nil)
		Expect(result.State).To(Equal(healthmonitoring.StateNoData))
		Expect(result.Profiles).To(HaveLen(1))
		Expect(result.Profiles[0].State).To(Equal(healthmonitoring.StateNoData))
		Expect(result.Profiles[0].Profile.Name).To(Equal("p1"))
	})

	It("does not use a report of a check the summary does not own as data", func() {
		summaries := []healthmonitoring.SummaryInput{
			summaryInput("s1", "p1", true, ""),
		}
		reports := []healthmonitoring.ReportInput{{HealthCheckName: generatedCheckName}}

		result := healthmonitoring.Evaluate(summaries, reports)
		Expect(result.Profiles[0].State).To(Equal(healthmonitoring.StateNoData))
		Expect(result.State).To(Equal(healthmonitoring.StateNoData))
	})

	It("returns Healthy for a profile whose report has no unhealthy resources", func() {
		summaries := []healthmonitoring.SummaryInput{
			summaryInput("s1", "p1", true, generatedCheckName),
		}
		reports := []healthmonitoring.ReportInput{{HealthCheckName: generatedCheckName}}

		result := healthmonitoring.Evaluate(summaries, reports)
		Expect(result.State).To(Equal(healthmonitoring.StateHealthy))
		Expect(result.Profiles[0].State).To(Equal(healthmonitoring.StateHealthy))
		Expect(result.Resources).To(BeEmpty())
	})

	It("attributes resources of the generated check and of a listed check to the profile", func() {
		summaries := []healthmonitoring.SummaryInput{
			summaryInput("s1", "p1", true, generatedCheckName, listedCheck),
		}
		reports := []healthmonitoring.ReportInput{
			{HealthCheckName: generatedCheckName, Resources: []healthmonitoring.ResourceInput{
				resourceInput("Deployment", "web", libsveltosv1beta1.HealthStatusDegraded, "no replicas"),
			}},
			{HealthCheckName: listedCheck, Resources: []healthmonitoring.ResourceInput{
				resourceInput("StatefulSet", "db", libsveltosv1beta1.HealthStatusProgressing, "rolling"),
			}},
		}

		result := healthmonitoring.Evaluate(summaries, reports)
		Expect(result.State).To(Equal(healthmonitoring.StateDegraded))
		Expect(result.Profiles[0].State).To(Equal(healthmonitoring.StateDegraded))
		Expect(result.Resources).To(HaveLen(2))
		for i := range result.Resources {
			Expect(result.Resources[i].Profiles).To(HaveLen(1))
			Expect(result.Resources[i].Profiles[0].Name).To(Equal("p1"))
			Expect(result.Resources[i].HealthCheckName).To(BeEmpty())
		}
	})

	It("lists a resource of a shared check once, under every profile listing the check", func() {
		summaries := []healthmonitoring.SummaryInput{
			summaryInput("s1", "p1", true, generatedCheckName, "shared"),
			summaryInput("s2", "p2", true, "hm-2", "shared"),
		}
		reports := []healthmonitoring.ReportInput{
			{HealthCheckName: "shared", Resources: []healthmonitoring.ResourceInput{
				resourceInput("Deployment", "web", libsveltosv1beta1.HealthStatusDegraded, "bad"),
			}},
			{HealthCheckName: generatedCheckName},
		}

		result := healthmonitoring.Evaluate(summaries, reports)
		Expect(result.Resources).To(HaveLen(1))
		Expect(result.Resources[0].Profiles).To(HaveLen(2))
		Expect(result.Profiles[0].State).To(Equal(healthmonitoring.StateDegraded))
		Expect(result.Profiles[1].State).To(Equal(healthmonitoring.StateDegraded))
	})

	It("lists the resources of a report no profile owns as unclaimed and counts them in the cluster state", func() {
		summaries := []healthmonitoring.SummaryInput{
			summaryInput("s1", "p1", true, generatedCheckName),
		}
		reports := []healthmonitoring.ReportInput{
			{HealthCheckName: generatedCheckName},
			{HealthCheckName: "user-check", Resources: []healthmonitoring.ResourceInput{
				resourceInput("Deployment", "web", libsveltosv1beta1.HealthStatusSuspended, "paused"),
			}},
		}

		result := healthmonitoring.Evaluate(summaries, reports)
		Expect(result.Profiles[0].State).To(Equal(healthmonitoring.StateHealthy))
		Expect(result.Resources).To(HaveLen(1))
		Expect(result.Resources[0].Profiles).To(BeEmpty())
		Expect(result.Resources[0].HealthCheckName).To(Equal("user-check"))
		Expect(result.State).To(Equal(healthmonitoring.StateSuspended))
	})

	It("ignores the reports of a summary with monitoring disabled", func() {
		summaries := []healthmonitoring.SummaryInput{
			summaryInput("s1", "p1", true, generatedCheckName),
			summaryInput("s2", "p2", false, "hm-2"),
		}
		reports := []healthmonitoring.ReportInput{
			{HealthCheckName: generatedCheckName},
		}

		result := healthmonitoring.Evaluate(summaries, reports)
		Expect(result.Profiles).To(HaveLen(1))
		Expect(result.Profiles[0].Profile.Name).To(Equal("p1"))
	})

	It("keeps the worst status of a resource reported by two checks", func() {
		summaries := []healthmonitoring.SummaryInput{
			summaryInput("s1", "p1", true, generatedCheckName, listedCheck),
		}
		reports := []healthmonitoring.ReportInput{
			{HealthCheckName: generatedCheckName, Resources: []healthmonitoring.ResourceInput{
				resourceInput("Deployment", "web", libsveltosv1beta1.HealthStatusProgressing, "rolling"),
			}},
			{HealthCheckName: listedCheck, Resources: []healthmonitoring.ResourceInput{
				resourceInput("Deployment", "web", libsveltosv1beta1.HealthStatusDegraded, "crashing"),
			}},
		}

		result := healthmonitoring.Evaluate(summaries, reports)
		Expect(result.Resources).To(HaveLen(1))
		Expect(result.Resources[0].Status).To(Equal(libsveltosv1beta1.HealthStatusDegraded))
		Expect(result.Resources[0].Message).To(Equal("crashing"))
		Expect(result.Resources[0].Profiles).To(HaveLen(1))
	})

	It("does not list Healthy resources", func() {
		summaries := []healthmonitoring.SummaryInput{
			summaryInput("s1", "p1", true, generatedCheckName),
		}
		reports := []healthmonitoring.ReportInput{
			{HealthCheckName: generatedCheckName, Resources: []healthmonitoring.ResourceInput{
				resourceInput("Deployment", "web", libsveltosv1beta1.HealthStatusHealthy, ""),
			}},
		}

		result := healthmonitoring.Evaluate(summaries, reports)
		Expect(result.State).To(Equal(healthmonitoring.StateHealthy))
		Expect(result.Resources).To(BeEmpty())
	})

	It("sorts the resources worst status first, then by kind, namespace and name", func() {
		summaries := []healthmonitoring.SummaryInput{
			summaryInput("s1", "p1", true, generatedCheckName),
		}
		reports := []healthmonitoring.ReportInput{
			{HealthCheckName: generatedCheckName, Resources: []healthmonitoring.ResourceInput{
				resourceInput("Deployment", "b", libsveltosv1beta1.HealthStatusProgressing, ""),
				resourceInput("StatefulSet", "a", libsveltosv1beta1.HealthStatusDegraded, ""),
				resourceInput("Deployment", "a", libsveltosv1beta1.HealthStatusProgressing, ""),
				resourceInput("Deployment", "c", libsveltosv1beta1.HealthStatusDegraded, ""),
			}},
		}

		result := healthmonitoring.Evaluate(summaries, reports)
		names := make([]string, 0, len(result.Resources))
		for i := range result.Resources {
			names = append(names, result.Resources[i].ObjectRef.Kind+"/"+result.Resources[i].ObjectRef.Name)
		}
		Expect(names).To(Equal([]string{"Deployment/c", "StatefulSet/a", "Deployment/a", "Deployment/b"}))
	})

	It("evaluates 100 summaries and reports", func() {
		summaries := make([]healthmonitoring.SummaryInput, 0, 100)
		reports := make([]healthmonitoring.ReportInput, 0, 100)
		for i := 0; i < 100; i++ {
			check := "hm-" + string(rune('a'+i%26)) + string(rune('a'+i/26))
			summaries = append(summaries, summaryInput(check, check, true, check))
			reports = append(reports, healthmonitoring.ReportInput{HealthCheckName: check})
		}

		result := healthmonitoring.Evaluate(summaries, reports)
		Expect(result.Profiles).To(HaveLen(100))
		Expect(result.State).To(Equal(healthmonitoring.StateHealthy))
	})
})

var _ = Describe("WorstState", func() {
	It("orders the states from NotEnabled to Degraded", func() {
		ordered := []healthmonitoring.State{
			healthmonitoring.StateNotEnabled, healthmonitoring.StateNoData, healthmonitoring.StateHealthy,
			healthmonitoring.StateSuspended, healthmonitoring.StateProgressing, healthmonitoring.StateDegraded,
		}
		for i := range ordered {
			for j := range ordered {
				worst := ordered[i]
				if j > i {
					worst = ordered[j]
				}
				Expect(healthmonitoring.WorstState(ordered[i], ordered[j])).To(Equal(worst))
			}
		}
	})
})

var _ = Describe("StateOf", func() {
	It("maps every health status", func() {
		Expect(healthmonitoring.StateOf(libsveltosv1beta1.HealthStatusHealthy)).To(Equal(healthmonitoring.StateHealthy))
		Expect(healthmonitoring.StateOf(libsveltosv1beta1.HealthStatusSuspended)).To(Equal(healthmonitoring.StateSuspended))
		Expect(healthmonitoring.StateOf(libsveltosv1beta1.HealthStatusProgressing)).To(Equal(healthmonitoring.StateProgressing))
		Expect(healthmonitoring.StateOf(libsveltosv1beta1.HealthStatusDegraded)).To(Equal(healthmonitoring.StateDegraded))
	})
})

var _ = Describe("GeneratedCheckOwner", func() {
	It("returns the ClusterSummary a generated HealthCheck belongs to", func() {
		healthCheck := &libsveltosv1beta1.HealthCheck{
			ObjectMeta: metav1.ObjectMeta{
				Name:   "hm-abc",
				Labels: map[string]string{configv1beta1.HealthMonitoringClusterSummaryUIDLabel: testUID},
				Annotations: map[string]string{
					configv1beta1.HealthMonitoringClusterSummaryNamespaceAnnotation: testNamespace,
					configv1beta1.HealthMonitoringClusterSummaryNameAnnotation:      summaryName,
				},
			},
		}

		ref, ok := healthmonitoring.GeneratedCheckOwner(healthCheck)
		Expect(ok).To(BeTrue())
		Expect(ref.Kind).To(Equal(configv1beta1.ClusterSummaryKind))
		Expect(ref.Namespace).To(Equal(testNamespace))
		Expect(ref.Name).To(Equal(summaryName))
	})

	It("returns false without the UID label or without the annotations", func() {
		withoutLabel := &libsveltosv1beta1.HealthCheck{
			ObjectMeta: metav1.ObjectMeta{
				Annotations: map[string]string{
					configv1beta1.HealthMonitoringClusterSummaryNamespaceAnnotation: testNamespace,
					configv1beta1.HealthMonitoringClusterSummaryNameAnnotation:      summaryName,
				},
			},
		}
		_, ok := healthmonitoring.GeneratedCheckOwner(withoutLabel)
		Expect(ok).To(BeFalse())

		withoutAnnotations := &libsveltosv1beta1.HealthCheck{
			ObjectMeta: metav1.ObjectMeta{
				Labels: map[string]string{configv1beta1.HealthMonitoringClusterSummaryUIDLabel: testUID},
			},
		}
		_, ok = healthmonitoring.GeneratedCheckOwner(withoutAnnotations)
		Expect(ok).To(BeFalse())
	})
})

var _ = Describe("NewSummaryInput", func() {
	newSummary := func(profileKind string, healthMonitoring *configv1beta1.HealthMonitoring) *configv1beta1.ClusterSummary {
		return &configv1beta1.ClusterSummary{
			ObjectMeta: metav1.ObjectMeta{
				Namespace: testNamespace,
				Name:      summaryName,
				OwnerReferences: []metav1.OwnerReference{
					{APIVersion: configv1beta1.GroupVersion.String(), Kind: profileKind, Name: "profile"},
				},
			},
			Spec: configv1beta1.ClusterSummarySpec{
				ClusterProfileSpec: configv1beta1.Spec{HealthMonitoring: healthMonitoring},
			},
		}
	}

	It("keeps the listed checks of a ClusterProfile", func() {
		summary := newSummary(configv1beta1.ClusterProfileKind,
			&configv1beta1.HealthMonitoring{Enabled: true, HealthChecks: []string{"a", "b"}})

		input, err := healthmonitoring.NewSummaryInput(summary, generatedCheckName)
		Expect(err).To(BeNil())
		Expect(input.Enabled).To(BeTrue())
		Expect(input.ListedChecks).To(Equal([]string{"a", "b"}))
		Expect(input.GeneratedCheck).To(Equal(generatedCheckName))
		Expect(input.Summary.Namespace).To(Equal(testNamespace))
		Expect(input.Summary.Name).To(Equal(summaryName))
		Expect(input.Profile.Kind).To(Equal(configv1beta1.ClusterProfileKind))
		Expect(input.Profile.Name).To(Equal("profile"))
	})

	It("ignores the listed checks of a Profile and gives it the namespace of the ClusterSummary", func() {
		summary := newSummary(configv1beta1.ProfileKind,
			&configv1beta1.HealthMonitoring{Enabled: true, HealthChecks: []string{"a"}})

		input, err := healthmonitoring.NewSummaryInput(summary, "")
		Expect(err).To(BeNil())
		Expect(input.Enabled).To(BeTrue())
		Expect(input.ListedChecks).To(BeEmpty())
		Expect(input.Profile.Kind).To(Equal(configv1beta1.ProfileKind))
		Expect(input.Profile.Namespace).To(Equal(testNamespace))
	})

	It("is not enabled when health monitoring is unset or disabled", func() {
		input, err := healthmonitoring.NewSummaryInput(newSummary(configv1beta1.ClusterProfileKind, nil), "")
		Expect(err).To(BeNil())
		Expect(input.Enabled).To(BeFalse())

		input, err = healthmonitoring.NewSummaryInput(newSummary(configv1beta1.ClusterProfileKind,
			&configv1beta1.HealthMonitoring{Enabled: false}), "")
		Expect(err).To(BeNil())
		Expect(input.Enabled).To(BeFalse())
	})

	It("returns an error when the ClusterSummary has no profile owner", func() {
		summary := &configv1beta1.ClusterSummary{ObjectMeta: metav1.ObjectMeta{Namespace: testNamespace, Name: summaryName}}
		_, err := healthmonitoring.NewSummaryInput(summary, "")
		Expect(err).ToNot(BeNil())
	})
})

var _ = Describe("NewReportInput", func() {
	It("copies the health check name and the resource statuses", func() {
		report := &libsveltosv1beta1.HealthCheckReport{
			Spec: libsveltosv1beta1.HealthCheckReportSpec{
				HealthCheckName: generatedCheckName,
				ResourceStatuses: []libsveltosv1beta1.ResourceStatus{
					{
						ObjectRef:    corev1.ObjectReference{Kind: "Deployment", Namespace: "d", Name: "web"},
						HealthStatus: libsveltosv1beta1.HealthStatusDegraded,
						Message:      "bad",
						Resource:     []byte("big"),
					},
				},
			},
		}

		input := healthmonitoring.NewReportInput(report)
		Expect(input.HealthCheckName).To(Equal(generatedCheckName))
		Expect(input.Resources).To(HaveLen(1))
		Expect(input.Resources[0].ObjectRef.Name).To(Equal("web"))
		Expect(input.Resources[0].Status).To(Equal(libsveltosv1beta1.HealthStatusDegraded))
		Expect(input.Resources[0].Message).To(Equal("bad"))
	})
})
