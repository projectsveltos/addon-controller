package utils

import (
	"context"
	"regexp"
	"strings"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	configv1beta1 "github.com/projectsveltos/addon-controller/api/v1beta1"
)

func setupScheme() (*runtime.Scheme, error) {
	s := runtime.NewScheme()
	if err := configv1beta1.AddToScheme(s); err != nil {
		return nil, err
	}
	if err := clientgoscheme.AddToScheme(s); err != nil {
		return nil, err
	}
	return s, nil
}

func testEllipsizeFunc(t *testing.T, tests []struct {
	name  string
	input string
}, fn func(string) (string, error), suffixPattern *regexp.Regexp,
) {

	t.Helper()

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := fn(tt.input)
			if err != nil {
				t.Errorf("error not expected: %v", err)
			}

			if len(result) > maxNameLength {
				t.Errorf("result length = %d, want <= %d", len(result), maxNameLength)
			}

			if !suffixPattern.MatchString(result) {
				t.Errorf("result %q should match pattern %s", result, suffixPattern.String())
			}

			if result == tt.input {
				t.Errorf("result should differ from input")
			}

			if len(tt.input)+suffixLength > maxNameLength {
				if len(result) != maxNameLength {
					t.Errorf("result length = %d, want exactly %d for long input", len(result), maxNameLength)
				}
			}
		})
	}
}

func TestEllipsizeName(t *testing.T) {
	tests := []struct {
		name  string
		input string
	}{
		{
			name:  "short name gets hash",
			input: "short",
		},
		{
			name:  "medium name gets hash",
			input: "my-profile-capi-my-cluster",
		},
		{
			name:  "at max length gets truncated and hash",
			input: strings.Repeat("a", maxNameLength),
		},
		{
			name:  "over max length gets ellipsized",
			input: strings.Repeat("a", maxNameLength+1),
		},
		{
			name:  "very long name gets ellipsized",
			input: strings.Repeat("a", maxNameLength*2),
		},
		{
			name:  "real cluster summary name",
			input: "very-long-profile-name-that-exceeds-limits-capi-very-long-cluster-name-that-also-exceeds",
		},
		{
			name:  "real cluster report name",
			input: "p--very-long-profile-name-that-exceeds-limits--capi--very-long-cluster-name-that-also-exceeds",
		},
	}

	hashSuffixPattern := regexp.MustCompile(`-h[0-9a-f]{8}$`)
	testEllipsizeFunc(t, tests, EllipsizeName, hashSuffixPattern)
}

func TestEllipsizeNameWithRand(t *testing.T) {
	tests := []struct {
		name  string
		input string
	}{
		{
			name:  "short name gets random suffix",
			input: "short",
		},
		{
			name:  "medium name gets random suffix",
			input: "my-profile-capi-my-cluster",
		},
		{
			name:  "at max base length",
			input: strings.Repeat("a", maxBaseLength),
		},
		{
			name:  "over max base length gets truncated",
			input: strings.Repeat("a", maxBaseLength+1),
		},
		{
			name:  "very long name gets truncated",
			input: strings.Repeat("a", maxNameLength*2),
		},
		{
			name:  "real cluster summary name",
			input: "very-long-profile-name-that-exceeds-limits-capi-very-long-cluster-name-that-also-exceeds",
		},
		{
			name:  "ellipsized name with hash suffix",
			input: "very-long-profile-name-that-exceeds-limits-capi-very-l-h52674346",
		},
	}

	randomSuffixPattern := regexp.MustCompile(`-r[0-9a-f]{8}$`)
	testEllipsizeFunc(t, tests, EllipsizeNameWithRand, randomSuffixPattern)
}

func TestNameManager(t *testing.T) { //nolint:funlen // nested tests
	t.Run("with nil client", func(t *testing.T) {
		nm := &NameManager{}

		tests := []struct {
			name     string
			fullName string
		}{
			{
				name:     "short name",
				fullName: "short-name",
			},
			{
				name:     "long name gets ellipsized",
				fullName: "very-long-profile-name-that-exceeds-limits-capi-very-long-cluster-name",
			},
		}

		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				result, err := nm.AllocateName(context.Background(), "default", tt.fullName, nil)
				if err != nil {
					t.Errorf("error not expected: %v", err)
				}

				if len(result) > maxNameLength {
					t.Errorf("AllocateName() result length = %d, want <= %d", len(result), maxNameLength)
				}
			})
		}
	})

	t.Run("with client - no collision (no objects)", func(t *testing.T) {
		scheme, err := setupScheme()
		if err != nil {
			t.Fatalf("setupScheme() failed: %v", err)
		}

		c := fake.NewClientBuilder().WithScheme(scheme).Build()

		nm := &NameManager{}
		nm.SetClient(c)

		fullName := "my-profile-capi-my-cluster"
		result, err := nm.AllocateName(context.Background(), "default", fullName, &configv1beta1.ClusterSummary{})
		if err != nil {
			t.Errorf("error not expected: %v", err)
		}

		if len(result) > maxNameLength {
			t.Errorf("AllocateName() result length = %d, want <= %d", len(result), maxNameLength)
		}

		if result != fullName {
			t.Errorf("AllocateName() result = %q, want %q (no collision, name should stay same)", result, fullName)
		}
	})

	t.Run("with client - long name with collision", func(t *testing.T) {
		scheme, err := setupScheme()
		if err != nil {
			t.Fatalf("setupScheme() failed: %v", err)
		}

		namespace := "default"
		fullName := "very-long-profile-name-that-exceeds-limits-capi-very-long-cluster-name-that-also-exceeds"
		ellipsizedName, err := EllipsizeName(fullName)
		if err != nil {
			t.Errorf("error not expected: %v", err)
		}

		existingClusterSummary := &configv1beta1.ClusterSummary{
			ObjectMeta: metav1.ObjectMeta{
				Name:      ellipsizedName,
				Namespace: namespace,
			},
		}

		initObjects := []client.Object{existingClusterSummary}
		c := fake.NewClientBuilder().
			WithScheme(scheme).
			WithObjects(initObjects...).
			Build()

		nm := &NameManager{}
		nm.SetClient(c)

		result, err := nm.AllocateName(context.Background(), namespace, fullName, &configv1beta1.ClusterSummary{})
		if err != nil {
			t.Errorf("error not expected: %v", err)
		}

		if len(result) > maxNameLength {
			t.Errorf("AllocateName() result length = %d, want <= %d", len(result), maxNameLength)
		}

		if len(result) != maxNameLength {
			t.Errorf("AllocateName() result length = %d, want exactly %d for long name with collision", len(result), maxNameLength)
		}

		if result == ellipsizedName {
			t.Errorf("AllocateName() result = %q, want different name due to collision", result)
		}

		randomSuffixPattern := regexp.MustCompile(`-r[0-9a-f]{8}$`)
		if !randomSuffixPattern.MatchString(result) {
			t.Errorf("AllocateName() result %q should end with -r<8hexchars> due to collision", result)
		}
	})
}
