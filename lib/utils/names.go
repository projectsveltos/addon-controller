package utils

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"fmt"
	"hash/fnv"
	"sync"

	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	maxNameLength = 63
	suffixLength  = 10 // dash + prefix letter + 8 hex chars
	maxBaseLength = maxNameLength - suffixLength
)

var (
	nameManager = &NameManager{
		cache: make(map[string]struct{}),
		mu:    sync.Mutex{},
	}
)

// NameManager handles ClusterSummary name allocation
type NameManager struct {
	client client.Client
	cache  map[string]struct{} // Set of allocated object names (namespace/name)
	mu     sync.Mutex
}

func GetNameManager() *NameManager {
	return nameManager
}

func (nm *NameManager) SetClient(c client.Client) {
	nm.mu.Lock()
	defer nm.mu.Unlock()
	nm.client = c
}

func getCacheKey(namespace, name string) string {
	return namespace + "/" + name
}

// RebuildCache rebuilds cache from existing objects on startup
func (nm *NameManager) RebuildCache(ctx context.Context, listObj client.ObjectList, fullNameAnnotation string) error {
	nm.mu.Lock()
	defer nm.mu.Unlock()

	if nm.client == nil {
		return fmt.Errorf("client not set")
	}

	if err := nm.client.List(ctx, listObj); err != nil {
		return fmt.Errorf("failed to list objects: %w", err)
	}

	// Extract items from the list using reflection
	items, err := extractItems(listObj)
	if err != nil {
		return err
	}

	for _, obj := range items {
		cacheKey := getCacheKey(obj.GetNamespace(), obj.GetName())
		nm.cache[cacheKey] = struct{}{}
	}

	return nil
}

// extractItems extracts items from an ObjectList
func extractItems(list client.ObjectList) ([]client.Object, error) {
	items, err := meta.ExtractList(list)
	if err != nil {
		return nil, fmt.Errorf("failed to extract items: %w", err)
	}

	objects := make([]client.Object, 0, len(items))
	for _, item := range items {
		if obj, ok := item.(client.Object); ok {
			objects = append(objects, obj)
		}
	}

	return objects, nil
}

// RemoveName removes name from cache to keep cache in sync
func (nm *NameManager) RemoveName(namespace, name string) {
	nm.mu.Lock()
	defer nm.mu.Unlock()

	cacheKey := getCacheKey(namespace, name)
	delete(nm.cache, cacheKey)
}

// AllocateName allocates a name with collision handling
func (nm *NameManager) AllocateName(ctx context.Context, namespace, fullName string, obj client.Object) (string, error) {
	nm.mu.Lock()
	defer nm.mu.Unlock()

	if len(fullName) <= maxNameLength {
		return fullName, nil
	}

	var (
		ellipsized string
		err        error
	)

	ellipsized, err = EllipsizeName(fullName)
	if err != nil {
		return "", err
	}

	cacheKey := getCacheKey(namespace, ellipsized)
	if _, exists := nm.cache[cacheKey]; exists {
		// Conflict in cache, use random suffix
		ellipsized, err = EllipsizeNameWithRand(ellipsized)
		if err != nil {
			return "", err
		}
	} else if nm.client != nil {
		if nm.exists(ctx, namespace, ellipsized, obj) {
			// Object exists in API, update cache and resolve conflict
			nm.cache[cacheKey] = struct{}{}
			ellipsized, err = EllipsizeNameWithRand(ellipsized)
			if err != nil {
				return "", err
			}
		}
	}

	cacheKey = getCacheKey(namespace, ellipsized)
	nm.cache[cacheKey] = struct{}{}

	return ellipsized, nil
}

func (nm *NameManager) exists(ctx context.Context, namespace, name string, obj client.Object) bool {
	err := nm.client.Get(ctx, types.NamespacedName{
		Namespace: namespace,
		Name:      name,
	}, obj)
	return err == nil
}

func truncateForSuffix(name string) string {
	runes := []rune(name)
	if len(runes) > maxBaseLength {
		return string(runes[:maxBaseLength])
	}
	return name
}

// EllipsizeNameWithRand creates a name with random suffix for collision resolution
func EllipsizeNameWithRand(base string) (string, error) {
	var randomBytes [4]byte
	if _, err := rand.Read(randomBytes[:]); err != nil {
		return "", fmt.Errorf("failed to generate random suffix: %w", err)
	}

	randomValue := binary.BigEndian.Uint32(randomBytes[:])

	return fmt.Sprintf("%s-r%08x",
		truncateForSuffix(base),
		randomValue,
	), nil
}

// EllipsizeName ensures a Kubernetes object name is <= 63 characters
func EllipsizeName(name string) (string, error) {
	h := fnv.New32a()

	_, err := h.Write([]byte(name))
	if err != nil {
		return "", fmt.Errorf("failed to write hash: %w", err)
	}

	return fmt.Sprintf("%s-h%08x",
		truncateForSuffix(name),
		h.Sum32(),
	), nil
}
