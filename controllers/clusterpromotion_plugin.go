//go:build enterprise

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
	"github.com/projectsveltos/sveltos-enterprise/clusterpromotion"
)

// Official Sveltos images are built with `-tags enterprise` and a checkout of the private
// sveltos-enterprise module available (CI only). This file is the only place in this
// package that imports it; a default build excludes this file entirely (see
// clusterpromotion_oss.go), so `go build` from a public clone never needs to resolve it.
func init() {
	reconcileClusterPromotionNormal = func(ctx context.Context, deps clusterPromotionEnterpriseDeps,
		promotionScope *scope.ClusterPromotionScope, isInFreeTopX bool, logger logr.Logger) reconcile.Result {

		enterpriseReconciler := clusterpromotion.Reconciler{
			Client:           deps.Client,
			Config:           deps.Config,
			Scheme:           deps.Scheme,
			EventRecorder:    deps.EventRecorder,
			SveltosNamespace: deps.SveltosNamespace,
		}

		return enterpriseReconciler.ReconcileNormal(ctx, promotionScope, isInFreeTopX, logger)
	}
}
