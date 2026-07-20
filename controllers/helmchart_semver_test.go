/*
Copyright 2025. projectsveltos.io. All rights reserved.

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

// Package-internal (not controllers_test): helmChartVersionDiff is a pure, dependency-free
// function, so it is table-tested directly with the standard library rather than through the
// Ginkgo/envtest suite used by the rest of this package's tests.
package controllers

import "testing"

const (
	v1_0_0      = "1.0.0"
	v1_1_0      = "1.1.0"
	v1_2_3      = "1.2.3"
	v1_2_4      = "1.2.4"
	v1_2_5      = "1.2.5"
	v1_9_9      = "1.9.9"
	v2_0_0      = "2.0.0"
	notAVersion = "not-a-version"
)

func TestHelmChartVersionDiff(t *testing.T) {
	tests := []struct {
		name            string
		current         string
		available       []string
		wantLatest      string // "" means expect nil
		wantLatestPatch string // "" means expect nil
		wantErr         bool
	}{
		{
			name:            "newer minor and same-minor patch both found",
			current:         v1_2_3,
			available:       []string{v1_2_3, v1_2_4, "1.3.0", v2_0_0},
			wantLatest:      v2_0_0,
			wantLatestPatch: v1_2_4,
		},
		{
			name:            "only a same-minor patch found",
			current:         v1_2_3,
			available:       []string{v1_2_3, v1_2_4, v1_2_5},
			wantLatest:      v1_2_5,
			wantLatestPatch: v1_2_5,
		},
		{
			name:      "already the latest",
			current:   v2_0_0,
			available: []string{v1_2_3, v1_9_9, v2_0_0},
		},
		{
			name:      "current not in available list but nothing newer",
			current:   v2_0_0,
			available: []string{v1_9_9},
		},
		{
			name:      "unparseable current version",
			current:   notAVersion,
			available: []string{v1_0_0},
			wantErr:   true,
		},
		{
			name:            "unparseable entries in available list are skipped",
			current:         v1_0_0,
			available:       []string{notAVersion, v1_1_0, "also-bad"},
			wantLatest:      v1_1_0,
			wantLatestPatch: "",
		},
		{
			name:       "stable current excludes prereleases",
			current:    v1_0_0,
			available:  []string{"1.1.0-rc.1"},
			wantLatest: "",
		},
		{
			name:            "prerelease current includes prereleases",
			current:         "1.0.0-rc.1",
			available:       []string{"1.0.0-rc.2", v1_0_0},
			wantLatest:      v1_0_0,
			wantLatestPatch: v1_0_0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			latest, latestPatch, err := helmChartVersionDiff(tt.current, tt.available)

			if tt.wantErr {
				if err == nil {
					t.Fatalf("expected an error, got none")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			if tt.wantLatest == "" {
				if latest != nil {
					t.Errorf("expected nil latest, got %v", latest)
				}
			} else if latest == nil || latest.String() != tt.wantLatest {
				t.Errorf("expected latest %q, got %v", tt.wantLatest, latest)
			}

			if tt.wantLatestPatch == "" {
				if latestPatch != nil {
					t.Errorf("expected nil latestPatch, got %v", latestPatch)
				}
			} else if latestPatch == nil || latestPatch.String() != tt.wantLatestPatch {
				t.Errorf("expected latestPatch %q, got %v", tt.wantLatestPatch, latestPatch)
			}
		})
	}
}
