package utils

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"fmt"
	"hash/fnv"
	"sync"

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
		mu: sync.Mutex{},
	}
)

// NameManager handles ClusterSummary name allocation
type NameManager struct {
	client client.Client
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

	if nm.client != nil && nm.exists(ctx, namespace, ellipsized, obj) {
		ellipsized, err = EllipsizeNameWithRand(ellipsized)
		if err != nil {
			return "", err
		}
	}

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
