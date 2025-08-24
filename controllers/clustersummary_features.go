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

	libsveltosv1beta1 "github.com/projectsveltos/libsveltos/api/v1beta1"
	"github.com/projectsveltos/libsveltos/lib/deployer"
)

var (
	featuresHandlers map[libsveltosv1beta1.FeatureID]feature
)

func RegisterFeatures(d deployer.DeployerInterface, setupLog logr.Logger) {
	err := d.RegisterFeatureID(string(libsveltosv1beta1.FeatureResources))
	if err != nil {
		setupLog.Error(err, "failed to register feature FeatureResources")
		os.Exit(1)
	}
	err = d.RegisterFeatureID(string(libsveltosv1beta1.FeatureHelm))
	if err != nil {
		setupLog.Error(err, "failed to register feature FeatureHelm")
		os.Exit(1)
	}

	err = d.RegisterFeatureID(string(libsveltosv1beta1.FeatureKustomize))
	if err != nil {
		setupLog.Error(err, "failed to register feature FeatureKustomize")
		os.Exit(1)
	}

	creatFeatureHandlerMaps()
}

func creatFeatureHandlerMaps() {
	featuresHandlers = make(map[libsveltosv1beta1.FeatureID]feature)

	featuresHandlers[libsveltosv1beta1.FeatureResources] = feature{id: libsveltosv1beta1.FeatureResources,
		currentHash: resourcesHash, deploy: deployResources, undeploy: undeployResources, getRefs: getResourceRefs}

	featuresHandlers[libsveltosv1beta1.FeatureHelm] = feature{id: libsveltosv1beta1.FeatureHelm,
		currentHash: helmHash, deploy: deployHelmCharts, undeploy: undeployHelmCharts, getRefs: getHelmRefs}

	featuresHandlers[libsveltosv1beta1.FeatureKustomize] = feature{id: libsveltosv1beta1.FeatureKustomize,
		currentHash: kustomizationHash, deploy: deployKustomizeRefs, undeploy: undeployKustomizeRefs,
		getRefs: getKustomizationRefs}
}

func getHandlersForFeature(featureID libsveltosv1beta1.FeatureID) feature {
	v, ok := featuresHandlers[featureID]
	if !ok {
		panic(fmt.Errorf("feature %s has no feature handler registered", featureID))
	}

	return v
}
