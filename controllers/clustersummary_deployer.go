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
	"reflect"

	"github.com/gdexlab/go-render/render"
	"github.com/go-logr/logr"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	configv1alpha1 "github.com/projectsveltos/cluster-api-feature-manager/api/v1alpha1"
	"github.com/projectsveltos/cluster-api-feature-manager/pkg/deployer"
	"github.com/projectsveltos/cluster-api-feature-manager/pkg/scope"
)

type getCurrentHash func(cctx context.Context, c client.Client, clusterSummaryScope *scope.ClusterSummaryScope,
	logger logr.Logger) ([]byte, error)

type feature struct {
	id          configv1alpha1.FeatureID
	currentHash getCurrentHash
	deploy      deployer.RequestHandler
}

func (r *ClusterSummaryReconciler) deployFeature(ctx context.Context, clusterSummaryScope *scope.ClusterSummaryScope,
	f feature) error {
	clusterSummary := clusterSummaryScope.ClusterSummary

	// Get hash of current configuration (at this very precise moment)
	currentHash, err := f.currentHash(ctx, r.Client, clusterSummaryScope, r.Log)
	if err != nil {
		return err
	}

	deployed := r.isFeatureDeployed(clusterSummaryScope, f.id)
	hash := r.getHash(clusterSummaryScope, f.id)

	if deployed && reflect.DeepEqual(hash, currentHash) {
		// feature is deployed and nothing has changed. Nothing to do.
		return nil
	}

	if reflect.DeepEqual(hash, currentHash) {
		// Configuration has not changed. Check if a result is available.
		result := r.Deployer.GetResult(ctx, clusterSummary.Spec.ClusterNamespace, clusterSummary.Spec.ClusterName,
			clusterSummaryScope.Name(), string(configv1alpha1.FeatureRole))
		r.updateFeatureStatus(clusterSummaryScope, configv1alpha1.FeatureRole, result, hash)
		if result.ResultStatus == deployer.Deployed || result.ResultStatus == deployer.InProgress {
			return nil
		}
	}

	// Getting here means either feature failed to be deployed or configuration has changed.
	// Feature must be (re)deployed.

	if err := r.Deployer.Deploy(ctx, clusterSummary.Spec.ClusterNamespace, clusterSummary.Spec.ClusterName,
		clusterSummary.Name, string(f.id), f.deploy); err != nil {
		return err
	}

	return nil
}

// isFeatureDeployed returns true if feature is marked as deployed (present in FeatureSummaries and status
// is set to Provisioned). In such case, hash corresponding to the deployed configuration is returned.
func (r *ClusterSummaryReconciler) isFeatureDeployed(clusterSummaryScope *scope.ClusterSummaryScope,
	featureID configv1alpha1.FeatureID) bool {
	clusterSummary := clusterSummaryScope.ClusterSummary

	for i := range clusterSummary.Status.FeatureSummaries {
		fs := clusterSummary.Status.FeatureSummaries[i]
		if fs.FeatureID == featureID {
			if fs.Status == configv1alpha1.FeatureStatusProvisioned {
				return true
			}
		}
	}
	return false
}

// getHash returns, if available, the hash corresponding to the featureID configuration last time it
// was processed.
func (r *ClusterSummaryReconciler) getHash(clusterSummaryScope *scope.ClusterSummaryScope,
	featureID configv1alpha1.FeatureID) []byte {
	clusterSummary := clusterSummaryScope.ClusterSummary

	for i := range clusterSummary.Status.FeatureSummaries {
		fs := clusterSummary.Status.FeatureSummaries[i]
		if fs.FeatureID == featureID {
			return fs.Hash
		}
	}
	return nil
}

func (r *ClusterSummaryReconciler) updateFeatureStatus(clusterSummaryScope *scope.ClusterSummaryScope,
	featureID configv1alpha1.FeatureID, result deployer.Result, hash []byte) {

	switch result.ResultStatus {
	case deployer.Deployed:
		clusterSummaryScope.SetFeatureStatus(featureID, configv1alpha1.FeatureStatusProvisioned, hash)
		clusterSummaryScope.SetFailureMessage(featureID, nil)
	case deployer.InProgress:
		clusterSummaryScope.SetFeatureStatus(featureID, configv1alpha1.FeatureStatusProvisioning, hash)
	case deployer.Failed:
		clusterSummaryScope.SetFeatureStatus(featureID, configv1alpha1.FeatureStatusFailed, hash)
		err := result.Err.Error()
		clusterSummaryScope.SetFailureMessage(featureID, &err)
	case deployer.Unavailable:
		// Nothing to do here
	}
}

///////////////////////////////////////////////////////////////////////////////////////////////////////////
//////// Per feature Hash functions
///////////////////////////////////////////////////////////////////////////////////////////////////////////

// workloadRoleHash returns the hash of all the ClusterSummary referenced WorkloadRole Specs.
func workloadRoleHash(ctx context.Context, c client.Client, clusterSummaryScope *scope.ClusterSummaryScope,
	logger logr.Logger) ([]byte, error) {
	h := sha256.New()
	var config string

	clusterSummary := clusterSummaryScope.ClusterSummary
	for i := range clusterSummary.Spec.ClusterFeatureSpec.WorkloadRoles {
		reference := &clusterSummary.Spec.ClusterFeatureSpec.WorkloadRoles[i]
		workloadRole := &configv1alpha1.WorkloadRole{}
		err := c.Get(ctx, types.NamespacedName{Name: reference.Name}, workloadRole)
		if err != nil {
			if apierrors.IsNotFound(err) {
				logger.Info(fmt.Sprintf("workloadRole %s does not exist yet", reference.Name))
				continue
			}
			logger.Error(err, fmt.Sprintf("failed to get workloadRole %s", reference.Name))
			return nil, err
		}

		config += render.AsCode(workloadRole.Spec)
	}

	h.Write([]byte(config))
	return h.Sum(nil), nil
}
