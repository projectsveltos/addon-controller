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

package controllers

import (
	"fmt"

	"github.com/Masterminds/semver/v3"
)

// helmChartVersionDiff compares currentVersion against every version string in
// availableVersions and reports the highest version that is strictly newer, both overall and
// within currentVersion's own major.minor line. Unparseable entries in availableVersions are
// silently skipped (callers may log this themselves; kept out of this function so it stays a
// pure, easily table-tested comparison). Prerelease versions are only considered when
// currentVersion is itself a prerelease, so a stable pin is never flagged as outdated by a
// prerelease build.
// Returns (nil, nil, nil) when currentVersion is already the latest.
func helmChartVersionDiff(currentVersion string, availableVersions []string,
) (latestOverall, latestPatchSameMinor *semver.Version, err error) {

	current, err := semver.NewVersion(currentVersion)
	if err != nil {
		return nil, nil, fmt.Errorf("current version %q is not valid semver: %w", currentVersion, err)
	}

	includePrerelease := current.Prerelease() != ""

	for i := range availableVersions {
		candidate, parseErr := semver.NewVersion(availableVersions[i])
		if parseErr != nil {
			continue
		}

		if candidate.Prerelease() != "" && !includePrerelease {
			continue
		}

		if !candidate.GreaterThan(current) {
			continue
		}

		if latestOverall == nil || candidate.GreaterThan(latestOverall) {
			latestOverall = candidate
		}

		if candidate.Major() == current.Major() && candidate.Minor() == current.Minor() {
			if latestPatchSameMinor == nil || candidate.GreaterThan(latestPatchSameMinor) {
				latestPatchSameMinor = candidate
			}
		}
	}

	return latestOverall, latestPatchSameMinor, nil
}
