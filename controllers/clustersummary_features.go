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
	"fmt"
	"os"

	"github.com/go-logr/logr"

	configv1alpha1 "github.com/projectsveltos/cluster-api-feature-manager/api/v1alpha1"
	"github.com/projectsveltos/cluster-api-feature-manager/pkg/deployer"
)

var (
	featuresHandlers map[configv1alpha1.FeatureID]feature
)

func RegisterFeatures(d deployer.DeployerInterface, setupLog logr.Logger) {
	err := d.RegisterFeatureID(string(configv1alpha1.FeatureResources))
	if err != nil {
		setupLog.Error(err, "failed to register feature FeatureResources")
		os.Exit(1)
	}
	err = d.RegisterFeatureID(string(configv1alpha1.FeatureKyverno))
	if err != nil {
		setupLog.Error(err, "failed to register feature FeatureKyerno")
		os.Exit(1)
	}
	err = d.RegisterFeatureID(string(configv1alpha1.FeatureGatekeeper))
	if err != nil {
		setupLog.Error(err, "failed to register feature FeatureGatekeeper")
		os.Exit(1)
	}
	err = d.RegisterFeatureID(string(configv1alpha1.FeaturePrometheus))
	if err != nil {
		setupLog.Error(err, "failed to register feature FeaturePrometheus")
		os.Exit(1)
	}
	err = d.RegisterFeatureID(string(configv1alpha1.FeatureContour))
	if err != nil {
		setupLog.Error(err, "failed to register feature FeatureContour")
		os.Exit(1)
	}

	creatFeatureHandlerMaps()
}

func creatFeatureHandlerMaps() {
	featuresHandlers = make(map[configv1alpha1.FeatureID]feature)

	featuresHandlers[configv1alpha1.FeaturePrometheus] = feature{id: configv1alpha1.FeaturePrometheus, currentHash: prometheusHash,
		deploy: deployPrometheus, undeploy: unDeployPrometheus, getRefs: getPrometheusRefs}

	featuresHandlers[configv1alpha1.FeatureKyverno] = feature{id: configv1alpha1.FeatureKyverno, currentHash: kyvernoHash,
		deploy: deployKyverno, undeploy: unDeployKyverno, getRefs: getKyvernoRefs}

	featuresHandlers[configv1alpha1.FeatureGatekeeper] = feature{id: configv1alpha1.FeatureGatekeeper, currentHash: gatekeeperHash,
		deploy: deployGatekeeper, undeploy: unDeployGatekeeper, getRefs: getGatekeeperRefs}

	featuresHandlers[configv1alpha1.FeatureContour] = feature{id: configv1alpha1.FeatureContour, currentHash: contourHash,
		deploy: deployContour, undeploy: unDeployContour, getRefs: getContourRefs}

	featuresHandlers[configv1alpha1.FeatureResources] = feature{id: configv1alpha1.FeatureResources, currentHash: resourcesHash,
		deploy: deployResources, undeploy: undeployResources, getRefs: getResourceRefs}
}

func getHandlersForFeature(featureID configv1alpha1.FeatureID) feature {
	v, ok := featuresHandlers[featureID]
	if !ok {
		panic(fmt.Errorf("feature %s has no feature handler registered", featureID))
	}

	return v
}
