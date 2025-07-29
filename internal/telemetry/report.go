/*
Copyright 2024 projectsveltos.io. All rights reserved.

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

package telemetry

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/pkg/errors"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/rest"
	clusterv1 "sigs.k8s.io/cluster-api/api/v1beta1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	configv1beta1 "github.com/projectsveltos/addon-controller/api/v1beta1"
	libsveltosv1beta1 "github.com/projectsveltos/libsveltos/api/v1beta1"
	logs "github.com/projectsveltos/libsveltos/lib/logsettings"
	libsveltostelemetry "github.com/projectsveltos/libsveltos/lib/telemetry"
)

//+kubebuilder:rbac:groups=lib.projectsveltos.io,resources=eventtriggers,verbs=get;list;watch
//+kubebuilder:rbac:groups=lib.projectsveltos.io,resources=clusterhealthchecks,verbs=get;list;watch

type instance struct {
	version string
	client.Client
	config *rest.Config
}

var (
	telemetryInstance *instance
	lock              = &sync.Mutex{}
)

const (
	contentTypeJSON = "application/json"
	domain          = "http://telemetry.projectsveltos.io/"
)

func StartCollecting(ctx context.Context, config *rest.Config, c client.Client, sveltosVersion string) error {
	if telemetryInstance == nil {
		lock.Lock()
		defer lock.Unlock()
		if telemetryInstance == nil {
			telemetryInstance = &instance{
				Client:  c,
				version: sveltosVersion,
				config:  config,
			}

			go telemetryInstance.reportData(ctx)
		}
	}

	return nil
}

// Collects telemetry data and send to to Sveltos telemetry server
func (m *instance) reportData(ctx context.Context) {
	// Data are collected 4 times a day
	const six = 6
	ticker := time.NewTicker(six * time.Hour)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			telemetryInstance.collectAndSendData(ctx)
		}
	}
}

func (m *instance) retrieveUUID(ctx context.Context) (string, error) {
	var sveltosNS corev1.Namespace
	if err := m.Client.Get(ctx, types.NamespacedName{Name: "projectsveltos"}, &sveltosNS); err != nil {
		return "", errors.Wrap(err, "cannot start the telemetry controller")
	}

	return string(sveltosNS.UID), nil
}

func (m *instance) collectAndSendData(ctx context.Context) {
	logger := log.FromContext(ctx)
	logger.V(logs.LogInfo).Info("collecting telemetry data")

	ctx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()

	uuid, err := telemetryInstance.retrieveUUID(ctx)
	if err != nil {
		return
	}

	payload, err := m.collectData(ctx, uuid)
	if err != nil {
		return
	}

	m.sendData(ctx, payload)
}

func (m *instance) collectData(ctx context.Context, uuid string) (*libsveltostelemetry.Cluster, error) {
	logger := log.FromContext(ctx)

	data := libsveltostelemetry.Cluster{
		UUID:           uuid,
		SveltosVersion: m.version,
	}

	var capiClusters clusterv1.ClusterList
	if err := m.Client.List(ctx, &capiClusters); err != nil {
		logger.V(logs.LogInfo).Info(fmt.Sprintf("failed to collect CAPI clusters: %v", err))
		return nil, err
	}
	data.ManagedCAPIClusters = len(capiClusters.Items)

	var sveltosClusters libsveltosv1beta1.SveltosClusterList
	if err := m.Client.List(ctx, &sveltosClusters); err != nil {
		logger.V(logs.LogInfo).Info(fmt.Sprintf("failed to collect sveltosclusters: %v", err))
		return nil, err
	}

	data.ManagedSveltosClusters = len(sveltosClusters.Items)

	for i := range sveltosClusters.Items {
		if sveltosClusters.Items[i].Status.Ready {
			data.ReadySveltosClusters++
		}
		if sveltosClusters.Items[i].Spec.PullMode {
			data.PullModeSveltosClusters++
		}
	}

	clusterProfiles, profiles, clusterSummaries, err := m.collectConfigurationData(ctx)
	if err != nil {
		logger.V(logs.LogInfo).Info(fmt.Sprintf("failed to collect Sveltos configuration data: %v", err))
		return nil, err
	}
	data.ClusterProfiles = clusterProfiles
	data.Profiles = profiles
	data.ClusterSummaries = clusterSummaries

	et, chc, err := m.collectEventData(ctx)
	if err != nil {
		logger.V(logs.LogInfo).Info(fmt.Sprintf("failed to collect Sveltos configuration data: %v", err))
	} else {
		data.EventTriggers = et
		data.ClusterHealthChecks = chc
	}

	return &data, nil
}

func (m *instance) collectConfigurationData(ctx context.Context) (cpInstances, pInstances, csInstances int, err error) {
	logger := log.FromContext(ctx)

	var clusterProfiles configv1beta1.ClusterProfileList
	if err = m.Client.List(ctx, &clusterProfiles); err != nil {
		logger.V(logs.LogInfo).Info(fmt.Sprintf("failed to collect clusterProfiles: %v", err))
		return
	}
	cpInstances = len(clusterProfiles.Items)

	var profiles configv1beta1.ProfileList
	if err = m.Client.List(ctx, &profiles); err != nil {
		logger.V(logs.LogInfo).Info(fmt.Sprintf("failed to collect Profiles: %v", err))
		return
	}
	pInstances = len(profiles.Items)

	var clusterSummaries configv1beta1.ClusterSummaryList
	if err = m.Client.List(ctx, &clusterSummaries); err != nil {
		logger.V(logs.LogInfo).Info(fmt.Sprintf("failed to collect ClusterSummaries: %v", err))
		return
	}
	csInstances = len(clusterSummaries.Items)

	return
}

func (m *instance) collectEventData(ctx context.Context) (eventTriggers, clusterHealthChecks int, err error) {
	logger := log.FromContext(ctx)

	d, err := dynamic.NewForConfig(m.config)
	if err != nil {
		return 0, 0, err
	}

	// Count EventTriggers
	eventTriggerGVR := schema.GroupVersionResource{
		Group:    "lib.projectsveltos.io",
		Version:  "v1beta1",
		Resource: "eventtriggers",
	}

	eventTriggerList, err := d.Resource(eventTriggerGVR).Namespace("").List(ctx, metav1.ListOptions{})
	if err == nil {
		eventTriggers = len(eventTriggerList.Items)
	} else {
		logger.V(logs.LogInfo).Info(fmt.Sprintf("failed to collect EventTriggers: %v", err))
	}

	// Count ClusterHealthChecks
	chcGVR := schema.GroupVersionResource{
		Group:    "lib.projectsveltos.io",
		Version:  "v1beta1",
		Resource: "clusterhealthchecks",
	}

	chcList, err := d.Resource(chcGVR).Namespace("").List(ctx, metav1.ListOptions{})
	if err == nil {
		clusterHealthChecks = len(chcList.Items)
	} else {
		logger.V(logs.LogInfo).Info(fmt.Sprintf("failed to collect ClusterHealthChecks: %v", err))
	}

	return eventTriggers, clusterHealthChecks, nil
}

func (m *instance) sendData(ctx context.Context, payload *libsveltostelemetry.Cluster) {
	logger := log.FromContext(ctx)

	data, err := json.Marshal(payload)
	if err != nil {
		return
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, domain, bytes.NewBuffer(data))
	if err != nil {
		return
	}

	req.Header.Set("Content-Type", contentTypeJSON)
	req.Header.Set("User-Agent", "projectsveltos/sveltos-telemetry")

	// Create an HTTP client with follow redirects enabled
	c := &http.Client{
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			// Follow redirect and set body
			newReq, err := http.NewRequestWithContext(ctx, http.MethodGet, domain, bytes.NewBuffer(data))
			req.Body = newReq.Body
			return err
		},
	}
	// Send the request
	resp, err := c.Do(req)
	if err != nil {
		logger.V(logs.LogInfo).Info(fmt.Sprintf("error sending data: %v", err))
		return
	}
	defer resp.Body.Close()
	logger.V(logs.LogInfo).Info(fmt.Sprintf("Response status code: %d", resp.StatusCode))
}
