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

import (
	"fmt"
	"time"

	lua "github.com/yuin/gopher-lua"
)

const (
	luaTableError = "lua script output is not a lua table"
	luaBoolError  = "lua script output is not a lua bool"
)

// mapToTable converts a Go map to a lua table
// credit to: https://github.com/yuin/gopher-lua/issues/160#issuecomment-447608033
func mapToTable(m map[string]interface{}) *lua.LTable {
	// Main table pointer
	resultTable := &lua.LTable{}

	// Loop map
	for key, element := range m {
		switch element := element.(type) {
		case float64:
			resultTable.RawSetString(key, lua.LNumber(element))
		case int64:
			resultTable.RawSetString(key, lua.LNumber(element))
		case string:
			resultTable.RawSetString(key, lua.LString(element))
		case bool:
			resultTable.RawSetString(key, lua.LBool(element))
		case []byte:
			resultTable.RawSetString(key, lua.LString(string(element)))
		case map[string]interface{}:

			// Get table from map
			tble := mapToTable(element)

			resultTable.RawSetString(key, tble)

		case time.Time:
			resultTable.RawSetString(key, lua.LNumber(element.Unix()))

		case []map[string]interface{}:

			// Create slice table
			sliceTable := &lua.LTable{}

			// Loop element
			for _, s := range element {
				// Get table from map
				tble := mapToTable(s)

				sliceTable.Append(tble)
			}

			// Set slice table
			resultTable.RawSetString(key, sliceTable)

		case []interface{}:

			// Create slice table
			sliceTable := &lua.LTable{}

			// Loop interface slice
			for _, s := range element {
				// Switch interface type
				switch s := s.(type) {
				case map[string]interface{}:

					// Convert map to table
					t := mapToTable(s)

					// Append result
					sliceTable.Append(t)

				case float64:

					// Append result as number
					sliceTable.Append(lua.LNumber(s))

				case string:

					// Append result as string
					sliceTable.Append(lua.LString(s))

				case bool:

					// Append result as bool
					sliceTable.Append(lua.LBool(s))
				}
			}

			// Append to main table
			resultTable.RawSetString(key, sliceTable)
		}
	}

	return resultTable
}

// toGoValue converts the given LValue to a Go object.
// Credit to: https://github.com/yuin/gluamapper/blob/master/gluamapper.go
func toGoValue(lv lua.LValue) interface{} {
	switch v := lv.(type) {
	case *lua.LNilType:
		return nil
	case lua.LBool:
		return bool(v)
	case lua.LString:
		return string(v)
	case lua.LNumber:
		return float64(v)
	case *lua.LTable:
		maxn := v.MaxN()
		if maxn == 0 { // table
			ret := make(map[string]interface{})
			v.ForEach(func(key, value lua.LValue) {
				keystr := fmt.Sprint(toGoValue(key))
				ret[keystr] = toGoValue(value)
			})
			return ret
		} else { // array
			ret := make([]interface{}, 0, maxn)
			for i := 1; i <= maxn; i++ {
				ret = append(ret, toGoValue(v.RawGetInt(i)))
			}
			return ret
		}
	default:
		return v
	}
}
