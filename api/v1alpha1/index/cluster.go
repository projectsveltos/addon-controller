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

package index

import (
	"context"
	"fmt"

	"github.com/pkg/errors"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	configv1alpha1 "github.com/projectsveltos/sveltos-manager/api/v1alpha1"
)

const (
	// ClusterNamespaceField is used by the ClusterSummary Controller to index ClusterSummary
	// by CAPI Cluster namespace, and add a watch on CAPI Cluster.
	ClusterNamespaceField = ".spec.clusterNamespace"

	// ClusterNameField is used by the ClusterSummary Controller to index ClusterSummary
	// by CAPI Cluster name, and add a watch on CAPI Cluster.
	ClusterNameField = ".spec.clusterName"
)

// ByClusterNamespace adds the CAPI Cluster namespace index to the
// managers cache.
func ByClusterNamespace(ctx context.Context, mgr ctrl.Manager) error {
	if err := mgr.GetCache().IndexField(ctx, &configv1alpha1.ClusterSummary{},
		ClusterNamespaceField,
		clusterSummaryByClusterNamespace,
	); err != nil {
		return errors.Wrap(err, "error setting index field")
	}

	return nil
}

// ByClusterNamespace adds the CAPI Cluster name index to the
// managers cache.
func ByClusterName(ctx context.Context, mgr ctrl.Manager) error {
	if err := mgr.GetCache().IndexField(ctx, &configv1alpha1.ClusterSummary{},
		ClusterNameField,
		clusterSummaryByClusterName,
	); err != nil {
		return errors.Wrap(err, "error setting index field")
	}

	return nil
}

func clusterSummaryByClusterNamespace(o client.Object) []string {
	clusterSummary, ok := o.(*configv1alpha1.ClusterSummary)
	if !ok {
		panic(fmt.Sprintf("Expected a ClusterSummary but got a %T", o))
	}

	return []string{clusterSummary.Spec.ClusterNamespace}
}

func clusterSummaryByClusterName(o client.Object) []string {
	clusterSummary, ok := o.(*configv1alpha1.ClusterSummary)
	if !ok {
		panic(fmt.Sprintf("Expected a ClusterSummary but got a %T", o))
	}

	return []string{clusterSummary.Spec.ClusterName}
}
