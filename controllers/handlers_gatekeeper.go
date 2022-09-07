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

package controllers

import (
	"context"
	"crypto/sha256"
	"fmt"

	"github.com/gdexlab/go-render/render"
	"github.com/go-logr/logr"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	configv1alpha1 "github.com/projectsveltos/cluster-api-feature-manager/api/v1alpha1"
	"github.com/projectsveltos/cluster-api-feature-manager/internal/gatekeeper"
	"github.com/projectsveltos/cluster-api-feature-manager/pkg/logs"
	"github.com/projectsveltos/cluster-api-feature-manager/pkg/scope"
)

func deployGatekeeper(ctx context.Context, c client.Client,
	clusterNamespace, clusterName, applicant, _ string,
	logger logr.Logger) error {

	// Get ClusterSummary that requested this
	clusterSummary, remoteClient, err := getClusterSummaryAndCAPIClusterClient(ctx, applicant, c, logger)
	if err != nil {
		return err
	}

	// First verify if gatekeeper is installed, if not install it
	present, ready, err := isGatekeeperReady(ctx, remoteClient, logger)
	if err != nil {
		logger.V(logs.LogInfo).Error(err, "Failed to verify presence of gatekeeper deployments")
		return err
	}

	if !present {
		err = deployGatekeeperInWorklaodCluster(ctx, remoteClient, logger)
		if err != nil {
			return err
		}

		err = applyAuditOptions(ctx, remoteClient, clusterSummary, logger)
		if err != nil {
			return err
		}
	}

	if !ready {
		return fmt.Errorf("gatekeeper deployments are not ready yet")
	}

	return nil
}

func unDeployGatekeeper(ctx context.Context, c client.Client,
	clusterNamespace, clusterName, applicant, _ string,
	logger logr.Logger) error {

	// Nothing specific to do
	return nil
}

// gatekeeperHash returns the hash of all the Gatekeeper referenced configmaps.
func gatekeeperHash(ctx context.Context, c client.Client, clusterSummaryScope *scope.ClusterSummaryScope,
	logger logr.Logger) ([]byte, error) {

	h := sha256.New()
	var config string

	clusterSummary := clusterSummaryScope.ClusterSummary
	if clusterSummary.Spec.ClusterFeatureSpec.GatekeeperConfiguration == nil {
		return h.Sum(nil), nil
	}

	for i := range clusterSummary.Spec.ClusterFeatureSpec.GatekeeperConfiguration.PolicyRefs {
		reference := &clusterSummary.Spec.ClusterFeatureSpec.GatekeeperConfiguration.PolicyRefs[i]
		configmap := &corev1.ConfigMap{}
		err := c.Get(ctx, types.NamespacedName{Namespace: reference.Namespace, Name: reference.Name}, configmap)
		if err != nil {
			if apierrors.IsNotFound(err) {
				logger.Info(fmt.Sprintf("configMap %s/%s does not exist yet",
					reference.Namespace, reference.Name))
				continue
			}
			logger.Error(err, fmt.Sprintf("failed to get configMap %s/%s",
				reference.Namespace, reference.Name))
			return nil, err
		}

		config += render.AsCode(configmap.Data)
	}

	h.Write([]byte(config))
	return h.Sum(nil), nil
}

func getGatekeeperRefs(clusterSummary *configv1alpha1.ClusterSummary) []corev1.ObjectReference {
	if clusterSummary.Spec.ClusterFeatureSpec.GatekeeperConfiguration != nil {
		return clusterSummary.Spec.ClusterFeatureSpec.GatekeeperConfiguration.PolicyRefs
	}
	return nil
}

// isGatekeeperReady checks whether gatekeeper deployments are present and ready
// Return present=false, ready=false if at least one deployment is not present.
// Return present=true, ready=false if at least one deployment is not ready.
// Return present=true, ready=true if all deployments are present and ready
func isGatekeeperReady(ctx context.Context, c client.Client, logger logr.Logger) (present, ready bool, err error) {
	present = true
	ready = true

	for i := range gatekeeper.Deployments {
		var tmpPresent, tmpReady bool
		tmpPresent, tmpReady, err = isDeploymentReady(ctx, c, gatekeeper.Namespace, gatekeeper.Deployments[i], logger)
		if err != nil {
			return
		}
		if !tmpPresent {
			present = false
		}
		if !tmpReady {
			ready = false
		}
	}

	return
}

func deployGatekeeperInWorklaodCluster(ctx context.Context, c client.Client,
	logger logr.Logger) error {

	if err := createNamespace(ctx, c, gatekeeper.Namespace); err != nil {
		return err
	}

	return deployDoc(ctx, c, gatekeeper.GatekeeperYAML, logger)
}

func applyAuditOptions(ctx context.Context, c client.Client,
	clusterSummary *configv1alpha1.ClusterSummary,
	logger logr.Logger) error {

	auditDeployment := &appsv1.Deployment{}
	err := c.Get(ctx,
		types.NamespacedName{Namespace: gatekeeper.Namespace, Name: gatekeeper.AuditDeployment},
		auditDeployment)
	if err != nil {
		logger.V(logs.LogInfo).Error(err, "failed to get gatekeeper audit deployment")
		return err
	}

	containers := auditDeployment.Spec.Template.Spec.Containers
	containers[0].Args = append(containers[0].Args,
		fmt.Sprintf("--audit-chunk-size=%d", clusterSummary.Spec.ClusterFeatureSpec.GatekeeperConfiguration.AuditChunkSize),
		fmt.Sprintf("--audit-interval=%d", clusterSummary.Spec.ClusterFeatureSpec.GatekeeperConfiguration.AuditInterval),
		fmt.Sprintf("--audit-from-cache=%t", clusterSummary.Spec.ClusterFeatureSpec.GatekeeperConfiguration.AuditFromCache))

	auditDeployment.Spec.Template.Spec.Containers = containers

	return c.Update(ctx, auditDeployment)
}
