/*
Copyright 2024-25. projectsveltos.io. All rights reserved.

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

package v1beta1

type NonRetriableError struct {
	Message string
}

func (r *NonRetriableError) Error() string {
	return r.Message
}

type HandOverError struct {
	Message string
}

func (r *HandOverError) Error() string {
	return r.Message
}

type TemplateInstantiationError struct {
	Message string
}

func (r *TemplateInstantiationError) Error() string {
	return r.Message
}
