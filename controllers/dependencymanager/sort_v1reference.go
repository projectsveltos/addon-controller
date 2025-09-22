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

package dependencymanager

import (
	corev1 "k8s.io/api/core/v1"
)

type SortedCorev1ObjectReference []corev1.ObjectReference

func (a SortedCorev1ObjectReference) Len() int      { return len(a) }
func (a SortedCorev1ObjectReference) Swap(i, j int) { a[i], a[j] = a[j], a[i] }
func (a SortedCorev1ObjectReference) Less(i, j int) bool {
	if a[i].Kind != a[j].Kind {
		return a[i].Kind < a[j].Kind
	}
	if a[i].Namespace != a[j].Namespace {
		return a[i].Namespace < a[j].Namespace
	}
	return a[i].Name < a[j].Name
}
