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

const (
	testNamespace       = "default"
	testLongProfileName = "very-long-profile-name-that-exceeds-limits-capi-very-long-cluster-name"
	fullNameAnnotation  = "projectsveltos.io/full-name"
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
		nm := &NameManager{
			cache: make(map[string]struct{}),
		}

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

		nm := &NameManager{
			cache: make(map[string]struct{}),
		}
		nm.SetClient(c)

		fullName := "my-profile-capi-my-cluster"
		result, err := nm.AllocateName(context.Background(), testNamespace, fullName, &configv1beta1.ClusterSummary{})
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

		namespace := testNamespace
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

		nm := &NameManager{
			cache: make(map[string]struct{}),
		}
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

func TestCacheFunctionality(t *testing.T) {
	t.Run("cache stores and retrieves names", testCacheStoresAndRetrievesNames)
	t.Run("RebuildCache populates cache from existing objects", testRebuildCachePopulatesCache)
	t.Run("cache handles different namespaces", testCacheHandlesDifferentNamespaces)
}

func testCacheStoresAndRetrievesNames(t *testing.T) {
	t.Helper()
	nm := &NameManager{
		cache: make(map[string]struct{}),
	}

	namespace := testNamespace
	fullName := testLongProfileName
	ellipsizedName, err := EllipsizeName(fullName)
	if err != nil {
		t.Fatalf("EllipsizeName() failed: %v", err)
	}

	// First allocation should compute and cache
	result1, err := nm.AllocateName(context.Background(), namespace, fullName, &configv1beta1.ClusterSummary{})
	if err != nil {
		t.Errorf("AllocateName() error = %v", err)
	}

	if result1 != ellipsizedName {
		t.Errorf("AllocateName() first call = %q, want %q", result1, ellipsizedName)
	}

	// Second allocation with same name should detect conflict and allocate different name
	result2, err := nm.AllocateName(context.Background(), namespace, fullName, &configv1beta1.ClusterSummary{})
	if err != nil {
		t.Errorf("AllocateName() error = %v", err)
	}

	if result2 == result1 {
		t.Errorf("AllocateName() second call should differ due to conflict, got same: %q", result2)
	}

	// Verify cache contains both entries
	cacheKey1 := getCacheKey(namespace, result1)
	if _, ok := nm.cache[cacheKey1]; !ok {
		t.Errorf("cache should contain key %q", cacheKey1)
	}

	cacheKey2 := getCacheKey(namespace, result2)
	if _, ok := nm.cache[cacheKey2]; !ok {
		t.Errorf("cache should contain key %q", cacheKey2)
	}
}

func testRebuildCachePopulatesCache(t *testing.T) {
	t.Helper()
	scheme, err := setupScheme()
	if err != nil {
		t.Fatalf("setupScheme() failed: %v", err)
	}

	namespace := testNamespace
	fullName1 := "very-long-profile-name-that-exceeds-limits-capi-cluster-one-that-also-exceeds"
	fullName2 := "another-very-long-profile-name-that-exceeds-limits-capi-cluster-two-that-also-exceeds"

	ellipsizedName1, err := EllipsizeName(fullName1)
	if err != nil {
		t.Fatalf("EllipsizeName() failed: %v", err)
	}

	ellipsizedName2, err := EllipsizeName(fullName2)
	if err != nil {
		t.Fatalf("EllipsizeName() failed: %v", err)
	}

	cs1, cs2 := createTestClusterSummaries(namespace, fullName1, fullName2, ellipsizedName1, ellipsizedName2)

	c := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(cs1, cs2).
		Build()

	nm := &NameManager{
		cache: make(map[string]struct{}),
	}
	nm.SetClient(c)

	// Rebuild cache
	err = nm.RebuildCache(context.Background(), &configv1beta1.ClusterSummaryList{}, fullNameAnnotation)
	if err != nil {
		t.Errorf("RebuildCache() error = %v", err)
	}

	verifyCacheEntries(t, nm, namespace, ellipsizedName1, ellipsizedName2)

	// AllocateName should detect conflict and generate new name with random suffix
	result, err := nm.AllocateName(context.Background(), namespace, fullName1, &configv1beta1.ClusterSummary{})
	if err != nil {
		t.Errorf("AllocateName() error = %v", err)
	}

	if result == ellipsizedName1 {
		t.Errorf("AllocateName() result = %q, should differ from cached name due to conflict", result)
	}

	// Verify result has random suffix format: name-r12345678
	if len(result) < suffixLength {
		t.Errorf("AllocateName() result too short: %q", result)
	}
	suffix := result[len(result)-suffixLength:]
	if suffix[0] != '-' || suffix[1] != 'r' {
		t.Errorf("AllocateName() result = %q should have random suffix format '-rXXXXXXXX', got suffix: %q", result, suffix)
	}
}

func createTestClusterSummaries(namespace, fullName1, fullName2, ellipsizedName1, ellipsizedName2 string) (cs1, cs2 *configv1beta1.ClusterSummary) {
	cs1 = &configv1beta1.ClusterSummary{
		ObjectMeta: metav1.ObjectMeta{
			Name:      ellipsizedName1,
			Namespace: namespace,
			Annotations: map[string]string{
				fullNameAnnotation: fullName1,
			},
		},
	}

	cs2 = &configv1beta1.ClusterSummary{
		ObjectMeta: metav1.ObjectMeta{
			Name:      ellipsizedName2,
			Namespace: namespace,
			Annotations: map[string]string{
				fullNameAnnotation: fullName2,
			},
		},
	}

	return
}

func verifyCacheEntries(t *testing.T, nm *NameManager, namespace, ellipsizedName1, ellipsizedName2 string) {
	t.Helper()
	cacheKey1 := getCacheKey(namespace, ellipsizedName1)
	if _, ok := nm.cache[cacheKey1]; !ok {
		t.Errorf("cache should contain key %q", cacheKey1)
	}

	cacheKey2 := getCacheKey(namespace, ellipsizedName2)
	if _, ok := nm.cache[cacheKey2]; !ok {
		t.Errorf("cache should contain key %q", cacheKey2)
	}
}

func testCacheHandlesDifferentNamespaces(t *testing.T) {
	t.Helper()
	nm := &NameManager{
		cache: make(map[string]struct{}),
	}

	fullName := testLongProfileName
	ellipsizedName1, err := EllipsizeName(fullName)
	if err != nil {
		t.Fatalf("EllipsizeName() failed: %v", err)
	}

	ellipsizedName2, err := EllipsizeNameWithRand(ellipsizedName1)
	if err != nil {
		t.Fatalf("EllipsizeNameWithRand() failed: %v", err)
	}

	// Manually populate cache with different namespaces
	nm.cache[getCacheKey("namespace1", ellipsizedName1)] = struct{}{}
	nm.cache[getCacheKey("namespace2", ellipsizedName2)] = struct{}{}

	// Allocate from namespace1 - should detect conflict and use different name
	result1, err := nm.AllocateName(context.Background(), "namespace1", fullName, &configv1beta1.ClusterSummary{})
	if err != nil {
		t.Errorf("AllocateName() error = %v", err)
	}

	if result1 == ellipsizedName1 {
		t.Errorf("AllocateName() namespace1 should differ from cached name due to conflict, got %q", result1)
	}

	// Allocate from namespace2 - should detect conflict and use different name
	result2, err := nm.AllocateName(context.Background(), "namespace2", fullName, &configv1beta1.ClusterSummary{})
	if err != nil {
		t.Errorf("AllocateName() error = %v", err)
	}

	if result2 == ellipsizedName2 {
		t.Errorf("AllocateName() namespace2 should differ from cached name due to conflict, got %q", result2)
	}

	// Results should be different from each other
	if result1 == result2 {
		t.Errorf("AllocateName() results should differ across namespaces")
	}
}
