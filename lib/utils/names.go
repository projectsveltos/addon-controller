package utils

import (
	"fmt"
	"hash/fnv"
)

// EllipsizeName ensures a Kubernetes object name is <= 63 characters.
// If the name exceeds 63 characters, it truncates and appends an 8-character FNV-32 hash
// for uniqueness.
func EllipsizeName(name string) string {
	const maxLength = 63
	if len(name) <= maxLength {
		return name
	}

	// Generate 8-char FNV-32 hex suffix
	h := fnv.New32a()
	h.Write([]byte(name))
	hash := fmt.Sprintf("%08x", h.Sum32())

	// Reserve 9 chars: 8 for hash + 1 for separator
	truncateLength := maxLength - 9
	truncated := name[:truncateLength]

	return truncated + "-" + hash
}
