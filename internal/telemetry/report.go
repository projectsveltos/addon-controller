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
	"k8s.io/apimachinery/pkg/types"
	clusterv1 "sigs.k8s.io/cluster-api/api/v1beta1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	libsveltosv1beta1 "github.com/projectsveltos/libsveltos/api/v1beta1"
	logs "github.com/projectsveltos/libsveltos/lib/logsettings"
	libsveltostelemetry "github.com/projectsveltos/libsveltos/lib/telemetry"
)

type instance struct {
	version string
	client.Client
}

var (
	telemetryInstance *instance
	lock              = &sync.Mutex{}
)

const (
	contentTypeJSON = "application/json"
	domain          = "http://telemetry.projectsveltos.io/"
	path            = "telemetry"
)

func StartCollecting(ctx context.Context, c client.Client, sveltosVersion string) error {
	if telemetryInstance == nil {
		lock.Lock()
		defer lock.Unlock()
		if telemetryInstance == nil {
			telemetryInstance = &instance{
				Client:  c,
				version: sveltosVersion,
			}

			go telemetryInstance.reportData(ctx)
		}
	}

	return nil
}

// Every hour collects telemetry data and send to to Sveltos telemetry server
func (m *instance) reportData(ctx context.Context) {
	ticker := time.NewTicker(time.Hour)
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
	}

	return &data, nil
}

func (m *instance) sendData(ctx context.Context, payload *libsveltostelemetry.Cluster) {
	logger := log.FromContext(ctx)

	data, err := json.Marshal(payload)
	if err != nil {
		return
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		fmt.Sprintf("%s/%s", domain, path), bytes.NewBuffer(data))
	if err != nil {
		return
	}

	req.Header.Set("Content-Type", contentTypeJSON)
	req.Header.Set("User-Agent", "projectsveltos/sveltos-telemetry")

	// Create an HTTP client
	c := &http.Client{}

	// Send the request
	resp, err := c.Do(req)
	if err != nil {
		logger.V(logs.LogInfo).Info(fmt.Sprintf("error sending data: %v", err))
		return
	}
	defer resp.Body.Close()
	logger.V(logs.LogInfo).Info(fmt.Sprintf("Response status code: %d", resp.StatusCode))
}
