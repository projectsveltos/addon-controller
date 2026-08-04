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

// Default ClusterPromotion implementation: reports the feature is unavailable. A Sveltos
// Enterprise build overrides this via SetClusterPromotionReconciler before starting the manager.
func init() {
	reconcileClusterPromotionNormal = func(_ context.Context, _ ClusterPromotionEnterpriseDeps,
		promotionScope *scope.ClusterPromotionScope, _ bool, logger logr.Logger) reconcile.Result {

		logger.V(logs.LogInfo).Info("ClusterPromotion requires a Sveltos Enterprise build")
		return reconcile.Result{RequeueAfter: licenseRequeueAfter}
	}
}
