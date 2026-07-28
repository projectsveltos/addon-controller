//go:build !enterprise

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

package controllers

import (
	"context"

	"github.com/go-logr/logr"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	"github.com/projectsveltos/addon-controller/pkg/scope"
	logs "github.com/projectsveltos/libsveltos/lib/logsettings"
)

// Default (non-"enterprise") build: ClusterPromotion is a Sveltos Enterprise feature and
// its implementation lives in the private sveltos-enterprise module, which this build
// does not import. Building official images requires `-tags enterprise` with a checkout
// of sveltos-enterprise available; see clusterpromotion_plugin.go.
func init() {
	reconcileClusterPromotionNormal = func(_ context.Context, _ clusterPromotionEnterpriseDeps,
		promotionScope *scope.ClusterPromotionScope, _ bool, logger logr.Logger) reconcile.Result {

		logger.V(logs.LogInfo).Info("ClusterPromotion requires a Sveltos Enterprise build")
		return reconcile.Result{RequeueAfter: licenseRequeueAfter}
	}
}
