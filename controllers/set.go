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

type Set struct {
	data map[string]bool
}

func (s *Set) init() {
	if s.data == nil {
		s.data = make(map[string]bool, 0)
	}
}

// insert adds entry to set
func (s *Set) insert(entry string) {
	s.init()
	s.data[entry] = true
}

// erase removes entry from set
func (s *Set) erase(entry string) {
	s.init()
	delete(s.data, entry)
}

// has returns true if entry is currently part of set
func (s *Set) has(entry string) bool {
	s.init()
	_, ok := s.data[entry]
	return ok
}

// len returns length of set
func (s *Set) len() int {
	return len(s.data)
}

// items returns a slice with all elements currently in set
func (s *Set) items() []string {
	keys := make([]string, s.len())

	i := 0
	for k := range s.data {
		keys[i] = k
		i++
	}

	return keys
}

// difference returns all elements which are in s but not in b
func (s *Set) difference(b Set) []string {
	results := make([]string, 0)
	for entry := range s.data {
		if !b.has(entry) {
			results = append(results, entry)
		}
	}

	return results
}
