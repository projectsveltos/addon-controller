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
	"time"

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"sigs.k8s.io/controller-runtime/pkg/client"

	configv1beta1 "github.com/projectsveltos/addon-controller/api/v1beta1"
	libsveltosv1beta1 "github.com/projectsveltos/libsveltos/api/v1beta1"
	logs "github.com/projectsveltos/libsveltos/lib/logsettings"
)

const (
	// clusterProfileNameLabel is set by ClusterPromotion (Sveltos Enterprise) on the ConfigMaps/Secrets it
	// creates for a stage, with the name of the ClusterProfile consuming them. The same key is used as
	// annotation holding the full name (a label value is limited to 63 characters).
	clusterProfileNameLabel = "config.projectsveltos.io/clusterprofilename"

	// clusterPromotionCopiesSweepInterval is how often ConfigMaps/Secrets created by ClusterPromotion are
	// checked
	clusterPromotionCopiesSweepInterval = 5 * time.Minute

	// clusterPromotionCopiesGracePeriod is how old a ConfigMap/Secret needs to be before it can be removed.
	// ClusterPromotion creates them just before the ClusterProfile consuming them.
	clusterPromotionCopiesGracePeriod = 5 * time.Minute
)

// ClusterPromotionCopiesSweeper periodically removes the ConfigMaps/Secrets ClusterPromotion created for
// a stage when they are not needed anymore: the ClusterProfile they were created for is gone, or does
// not reference them anymore. It is a safety net. Normally they are removed by the Kubernetes garbage
// collector when the ClusterPromotion is deleted, and by ClusterPromotion itself when it stops
// referencing them. It does not rely on the ClusterPromotion still existing.
type ClusterPromotionCopiesSweeper struct {
	client.Client
	Interval    time.Duration
	GracePeriod time.Duration
	Logger      logr.Logger
}

// NewClusterPromotionCopiesSweeper returns a sweeper using the default interval and grace period
func NewClusterPromotionCopiesSweeper(c client.Client, logger logr.Logger) *ClusterPromotionCopiesSweeper {
	return &ClusterPromotionCopiesSweeper{
		Client:      c,
		Interval:    clusterPromotionCopiesSweepInterval,
		GracePeriod: clusterPromotionCopiesGracePeriod,
		Logger:      logger,
	}
}

// Start implements manager.Runnable. It runs until ctx is done. Added with manager.Add, it only runs
// on the leader and after the caches are started.
func (s *ClusterPromotionCopiesSweeper) Start(ctx context.Context) error {
	ticker := time.NewTicker(s.Interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			s.Logger.Info("stopping ClusterPromotion copies sweeper")
			return nil
		case <-ticker.C:
			s.sweep(ctx)
		}
	}
}

func (s *ClusterPromotionCopiesSweeper) sweep(ctx context.Context) {
	// ClusterProfiles already looked up during this sweep. A nil entry means not found.
	clusterProfiles := map[string]*configv1beta1.ClusterProfile{}

	listOptions := []client.ListOption{client.HasLabels{clusterProfileNameLabel}}

	configMaps := &corev1.ConfigMapList{}
	if err := s.List(ctx, configMaps, listOptions...); err != nil {
		s.Logger.V(logs.LogInfo).Info("failed to list ConfigMaps", "error", err)
	} else {
		for i := range configMaps.Items {
			s.removeIfStale(ctx, &configMaps.Items[i], string(libsveltosv1beta1.ConfigMapReferencedResourceKind),
				clusterProfiles)
		}
	}

	secrets := &corev1.SecretList{}
	if err := s.List(ctx, secrets, listOptions...); err != nil {
		s.Logger.V(logs.LogInfo).Info("failed to list Secrets", "error", err)
	} else {
		for i := range secrets.Items {
			s.removeIfStale(ctx, &secrets.Items[i], string(libsveltosv1beta1.SecretReferencedResourceKind),
				clusterProfiles)
		}
	}
}

func (s *ClusterPromotionCopiesSweeper) removeIfStale(ctx context.Context, obj client.Object, kind string,
	clusterProfiles map[string]*configv1beta1.ClusterProfile) {

	logger := s.Logger.WithValues("kind", kind, "namespace", obj.GetNamespace(), "name", obj.GetName())

	stale, err := s.isStale(ctx, obj, kind, clusterProfiles)
	if err != nil {
		logger.V(logs.LogInfo).Info("failed to verify whether it is stale", "error", err)
		return
	}
	if !stale {
		return
	}

	logger.V(logs.LogInfo).Info("removing resource created by ClusterPromotion: ClusterProfile does not use it anymore")
	uid := obj.GetUID()
	err = s.Delete(ctx, obj, client.Preconditions{UID: &uid})
	if err != nil && !apierrors.IsNotFound(err) {
		logger.V(logs.LogInfo).Info("failed to delete", "error", err)
	}
}

// isStale returns true if the ClusterProfile the resource was created for does not exist, or does
// not reference the resource anymore. A ClusterProfile being deleted still exists: the resource is
// kept until it is gone.
func (s *ClusterPromotionCopiesSweeper) isStale(ctx context.Context, obj client.Object, kind string,
	clusterProfiles map[string]*configv1beta1.ClusterProfile) (bool, error) {

	// The annotation holds the full ClusterProfile name. Resources without it are not ours.
	clusterProfileName := obj.GetAnnotations()[clusterProfileNameLabel]
	if clusterProfileName == "" {
		return false, nil
	}

	if time.Since(obj.GetCreationTimestamp().Time) < s.GracePeriod {
		return false, nil
	}

	clusterProfile, ok := clusterProfiles[clusterProfileName]
	if !ok {
		clusterProfile = &configv1beta1.ClusterProfile{}
		err := s.Get(ctx, client.ObjectKey{Name: clusterProfileName}, clusterProfile)
		if err != nil {
			if !apierrors.IsNotFound(err) {
				return false, err
			}
			clusterProfile = nil
		}
		clusterProfiles[clusterProfileName] = clusterProfile
	}

	if clusterProfile == nil {
		return true, nil
	}

	references := getClusterPromotionReferences(&configv1beta1.ProfileSpec{
		PolicyRefs:        clusterProfile.Spec.PolicyRefs,
		KustomizationRefs: clusterProfile.Spec.KustomizationRefs,
		HelmCharts:        clusterProfile.Spec.HelmCharts,
		PatchesFrom:       clusterProfile.Spec.PatchesFrom,
	})
	for i := range references {
		if references[i].Kind == kind && references[i].Namespace == obj.GetNamespace() &&
			references[i].Name == obj.GetName() {

			return false, nil
		}
	}

	return true, nil
}
