// Package skipregistry provides a registry for managing directories that should skip linting and/or testing.
package skipregistry

import (
	"errors"
	"fmt"
	"path/filepath"
)

// SkipType represents what type of operations to skip.
type SkipType string

const (
	// SkipTypeLint indicates that linting should be skipped.
	SkipTypeLint SkipType = "lint"
	// SkipTypeTest indicates that testing should be skipped.
	SkipTypeTest SkipType = "test"
	// SkipTypeAll indicates that both linting and testing should be skipped.
	SkipTypeAll SkipType = "all"
)

// DirectoryPath represents an absolute directory path.
type DirectoryPath string

// Validate ensures the DirectoryPath is absolute.
func (dp DirectoryPath) Validate() error {
	if !filepath.IsAbs(string(dp)) {
		return fmt.Errorf("path must be absolute: %s", dp)
	}
	return nil
}

// String returns the string representation of DirectoryPath.
func (dp DirectoryPath) String() string {
	return string(dp)
}

// RegistryEntry represents a single entry in the skip registry.
type RegistryEntry struct {
	Path  DirectoryPath `json:"path"`
	Types []SkipType    `json:"types"`
}

// RegistryData represents the JSON structure of the registry file.
type RegistryData map[string][]string

// Custom errors for better error handling.
var (
	// ErrInvalidPath indicates an invalid directory path.
	ErrInvalidPath = errors.New("invalid path")
	// ErrNotFound indicates the requested item was not found.
	ErrNotFound = errors.New("not found")
	// ErrAlreadyExists indicates the item already exists.
	ErrAlreadyExists = errors.New("already exists")
	// ErrInvalidSkipType indicates an invalid skip type was provided.
	ErrInvalidSkipType = errors.New("invalid skip type")
	// ErrRegistryCorrupted indicates the registry file is corrupted.
	ErrRegistryCorrupted = errors.New("registry corrupted")
)

// ParseSkipType converts a string to a SkipType.
func ParseSkipType(s string) (SkipType, error) {
	switch s {
	case string(SkipTypeLint):
		return SkipTypeLint, nil
	case string(SkipTypeTest):
		return SkipTypeTest, nil
	case string(SkipTypeAll):
		return SkipTypeAll, nil
	default:
		return "", fmt.Errorf("%w: %s", ErrInvalidSkipType, s)
	}
}

// expandSkipType converts SkipTypeAll to individual skip types.
func expandSkipType(skipType SkipType) []SkipType {
	if skipType == SkipTypeAll {
		return []SkipType{SkipTypeLint, SkipTypeTest}
	}
	return []SkipType{skipType}
}

// containsSkipType checks if a slice contains a specific skip type.
func containsSkipType(types []SkipType, target SkipType) bool {
	for _, t := range types {
		if t == target {
			return true
		}
	}
	return false
}

// removeSkipType removes a skip type from a slice.
func removeSkipType(types []SkipType, target SkipType) []SkipType {
	result := make([]SkipType, 0, len(types))
	for _, t := range types {
		if t != target {
			result = append(result, t)
		}
	}
	return result
}

// normalizeSkipTypes ensures no duplicates and converts between string and SkipType.
func normalizeSkipTypes(types []string) ([]SkipType, error) {
	seen := make(map[SkipType]bool)
	result := make([]SkipType, 0, len(types))

	for _, t := range types {
		skipType, err := ParseSkipType(t)
		if err != nil {
			return nil, err
		}
		if !seen[skipType] {
			seen[skipType] = true
			result = append(result, skipType)
		}
	}

	return result, nil
}

// skipTypesToStrings converts SkipTypes to strings.
func skipTypesToStrings(types []SkipType) []string {
	result := make([]string, len(types))
	for i, t := range types {
		result[i] = string(t)
	}
	return result
}
