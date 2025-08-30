package skipregistry

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

// JSONRegistry is the concrete implementation with thread safety backed by JSON storage.
type JSONRegistry struct {
	mu      sync.RWMutex
	storage Storage
	cache   RegistryData
	loaded  bool
}

// NewRegistry creates a new registry with the given storage backend.
func NewRegistry(storage Storage) *JSONRegistry {
	return &JSONRegistry{
		storage: storage,
		cache:   make(RegistryData),
		loaded:  false,
	}
}

// ensureLoaded loads the registry from storage if not already loaded.
func (r *JSONRegistry) ensureLoaded(ctx context.Context) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	if r.loaded {
		return nil
	}

	data, err := r.storage.Load(ctx)
	if err != nil {
		// If file doesn't exist, start with empty registry
		if errors.Is(err, ErrNotFound) {
			r.cache = make(RegistryData)
			r.loaded = true
			return nil
		}
		return fmt.Errorf("load registry: %w", err)
	}

	r.cache = data
	r.loaded = true
	return nil
}

// IsSkipped checks if a directory has a specific skip type configured.
func (r *JSONRegistry) IsSkipped(ctx context.Context, dir DirectoryPath, skipType SkipType) (bool, error) {
	if err := dir.Validate(); err != nil {
		return false, fmt.Errorf("%w: %w", ErrInvalidPath, err)
	}

	if err := r.ensureLoaded(ctx); err != nil {
		return false, err
	}

	r.mu.RLock()
	defer r.mu.RUnlock()

	types, exists := r.cache[dir.String()]
	if !exists {
		return false, nil
	}

	// Check if the skip type exists
	for _, t := range types {
		st, parseErr := ParseSkipType(t)
		if parseErr != nil {
			continue
		}
		if st == skipType {
			return true, nil
		}
	}

	return false, nil
}

// GetSkipTypes returns all skip types configured for a directory.
func (r *JSONRegistry) GetSkipTypes(ctx context.Context, dir DirectoryPath) ([]SkipType, error) {
	if err := dir.Validate(); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrInvalidPath, err)
	}

	if err := r.ensureLoaded(ctx); err != nil {
		return nil, err
	}

	r.mu.RLock()
	defer r.mu.RUnlock()

	types, exists := r.cache[dir.String()]
	if !exists {
		return []SkipType{}, nil
	}

	// Convert strings to SkipTypes
	skipTypes, err := normalizeSkipTypes(types)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrRegistryCorrupted, err)
	}

	return skipTypes, nil
}

// ListAll returns all directories and their skip configurations.
func (r *JSONRegistry) ListAll(ctx context.Context) ([]RegistryEntry, error) {
	if err := r.ensureLoaded(ctx); err != nil {
		return nil, err
	}

	r.mu.RLock()
	defer r.mu.RUnlock()

	entries := make([]RegistryEntry, 0, len(r.cache))
	for path, types := range r.cache {
		skipTypes, err := normalizeSkipTypes(types)
		if err != nil {
			return nil, fmt.Errorf("%w: %w", ErrRegistryCorrupted, err)
		}

		entries = append(entries, RegistryEntry{
			Path:  DirectoryPath(path),
			Types: skipTypes,
		})
	}

	return entries, nil
}

// AddSkip adds a skip type to a directory.
func (r *JSONRegistry) AddSkip(ctx context.Context, dir DirectoryPath, skipType SkipType) error {
	if err := dir.Validate(); err != nil {
		return fmt.Errorf("%w: %w", ErrInvalidPath, err)
	}

	if err := r.ensureLoaded(ctx); err != nil {
		return err
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	// Get current types for the directory
	currentTypes, exists := r.cache[dir.String()]
	var skipTypes []SkipType
	if exists {
		normalizedTypes, err := normalizeSkipTypes(currentTypes)
		if err != nil {
			return fmt.Errorf("%w: %w", ErrRegistryCorrupted, err)
		}
		skipTypes = normalizedTypes
	}

	// Expand the skip type if it's "all"
	typesToAdd := expandSkipType(skipType)

	// Add new types if not already present
	modified := false
	for _, typeToAdd := range typesToAdd {
		if !containsSkipType(skipTypes, typeToAdd) {
			skipTypes = append(skipTypes, typeToAdd)
			modified = true
		}
	}

	if !modified {
		// Nothing to add, already exists
		return nil
	}

	// Update cache
	r.cache[dir.String()] = skipTypesToStrings(skipTypes)

	// Save to storage
	if saveErr := r.storage.Save(ctx, r.cache); saveErr != nil {
		// Revert cache on save failure
		if exists {
			r.cache[dir.String()] = currentTypes
		} else {
			delete(r.cache, dir.String())
		}
		return fmt.Errorf("save registry: %w", saveErr)
	}

	return nil
}

// RemoveSkip removes a skip type from a directory.
func (r *JSONRegistry) RemoveSkip(ctx context.Context, dir DirectoryPath, skipType SkipType) error {
	if err := dir.Validate(); err != nil {
		return fmt.Errorf("%w: %w", ErrInvalidPath, err)
	}

	if err := r.ensureLoaded(ctx); err != nil {
		return err
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	// Get current types for the directory
	currentTypes, exists := r.cache[dir.String()]
	if !exists {
		// Nothing to remove
		return nil
	}

	skipTypes, err := normalizeSkipTypes(currentTypes)
	if err != nil {
		return fmt.Errorf("%w: %w", ErrRegistryCorrupted, err)
	}

	// Expand the skip type if it's "all"
	typesToRemove := expandSkipType(skipType)

	// Remove specified types
	modified := false
	for _, typeToRemove := range typesToRemove {
		if containsSkipType(skipTypes, typeToRemove) {
			skipTypes = removeSkipType(skipTypes, typeToRemove)
			modified = true
		}
	}

	if !modified {
		// Nothing was removed
		return nil
	}

	// Update or remove from cache
	if len(skipTypes) == 0 {
		delete(r.cache, dir.String())
	} else {
		r.cache[dir.String()] = skipTypesToStrings(skipTypes)
	}

	// Save to storage
	if saveErr := r.storage.Save(ctx, r.cache); saveErr != nil {
		// Revert cache on save failure
		r.cache[dir.String()] = currentTypes
		return fmt.Errorf("save registry: %w", saveErr)
	}

	return nil
}

// Clear removes all skip configurations for a directory.
func (r *JSONRegistry) Clear(ctx context.Context, dir DirectoryPath) error {
	if err := dir.Validate(); err != nil {
		return fmt.Errorf("%w: %w", ErrInvalidPath, err)
	}

	if err := r.ensureLoaded(ctx); err != nil {
		return err
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	// Check if directory exists in cache
	currentTypes, exists := r.cache[dir.String()]
	if !exists {
		// Nothing to clear
		return nil
	}

	// Remove from cache
	delete(r.cache, dir.String())

	// Save to storage
	if saveErr := r.storage.Save(ctx, r.cache); saveErr != nil {
		// Revert cache on save failure
		r.cache[dir.String()] = currentTypes
		return fmt.Errorf("save registry: %w", saveErr)
	}

	return nil
}

// Helper function to get the registry file path.
func getRegistryPath() string {
	return filepath.Join(getClaudeDir(), "skip-registry.json")
}

// Helper function to get the ~/.claude directory.
func getClaudeDir() string {
	fs := newRealFileSystem()
	homeDir, err := fs.UserHomeDir()
	if err != nil {
		// Fallback to /tmp if we can't get home directory
		return filepath.Join(string(os.PathSeparator)+"tmp", ".claude")
	}
	return filepath.Join(homeDir, ".claude")
}
