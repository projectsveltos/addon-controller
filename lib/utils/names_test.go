package utils

import (
	"regexp"
	"strings"
	"testing"
	"unicode"
)

func TestEllipsizeName(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		wantSame bool // true if we expect output == input
	}{
		{
			name:     "short name unchanged",
			input:    "short",
			wantSame: true,
		},
		{
			name:     "exactly 63 chars unchanged",
			input:    strings.Repeat("a", 63),
			wantSame: true,
		},
		{
			name:     "64 chars gets ellipsized",
			input:    strings.Repeat("a", 64),
			wantSame: false,
		},
		{
			name:     "very long name gets ellipsized",
			input:    strings.Repeat("a", 100),
			wantSame: false,
		},
		{
			name:     "real cluster summary name",
			input:    "very-long-profile-name-that-exceeds-limits-capi-very-long-cluster-name-that-also-exceeds",
			wantSame: false,
		},
		{
			name:     "real cluster report name",
			input:    "p--very-long-profile-name-that-exceeds-limits--capi--very-long-cluster-name-that-also-exceeds",
			wantSame: false,
		},
	}

	hashSuffixPattern := regexp.MustCompile(`-[0-9a-f]{8}$`)

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := EllipsizeName(tt.input)

			// Check if result matches expectation
			if tt.wantSame && result != tt.input {
				t.Errorf("EllipsizeK8sName() = %q, want same as input %q", result, tt.input)
			}
			if !tt.wantSame && result == tt.input {
				t.Errorf("EllipsizeK8sName() = %q, expected different from input %q", result, tt.input)
			}

			// Check length constraint
			if len(result) > 63 {
				t.Errorf("EllipsizeK8sName() result length = %d, want <= 63", len(result))
			}

			// For ellipsized names, check hash suffix format
			if len(tt.input) > 63 && tt.input != "" {
				if len(result) != 63 {
					t.Errorf("EllipsizeK8sName() ellipsized result length = %d, want 63", len(result))
				}

				// Should end with -<8hexchars> pattern
				if !hashSuffixPattern.MatchString(result) {
					t.Errorf("EllipsizeK8sName() result %q should end with -<8hexchars>", result)
				}
			}
		})
	}
}

func isValidObjectName(name string) bool {
	if name == "" || len(name) > 63 {
		return false
	}

	isAlphanumeric := func(c rune) bool {
		return unicode.IsLower(c) || unicode.IsDigit(c)
	}

	if !isAlphanumeric(rune(name[0])) || !isAlphanumeric(rune(name[len(name)-1])) {
		return false
	}

	for _, c := range name {
		if !isAlphanumeric(c) && c != '-' && c != '.' {
			return false
		}
	}

	return true
}

func FuzzEllipsizeObjectNameHashCollisions(f *testing.F) {
	f.Add("very-long-profile-name-capi-cluster1" + strings.Repeat("x", 30))
	f.Add("very-long-profile-name-capi-cluster2" + strings.Repeat("x", 30))
	f.Add(strings.Repeat("a", 80))
	f.Add(strings.Repeat("b", 80))
	f.Add("p--profile-name--capi--cluster-name" + strings.Repeat("z", 50))

	results := make(map[string]string)

	f.Fuzz(func(t *testing.T, input string) {
		// Only care about inputs that will get hashed (longer than 63 chars)
		if len(input) <= 63 {
			t.Skip("Input will not be hashed")
		}

		if !isValidObjectName(input) {
			t.Skip("Input contains invalid characters for Kubernetes names")
		}

		result := EllipsizeName(input)

		// Check for hash collisions
		if existing, exists := results[result]; exists && existing != input {
			t.Errorf("Hash collision detected: inputs %q and %q both produced %q", input, existing, result)
		}

		results[result] = input
	})
}
