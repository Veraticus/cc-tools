package skipregistry

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// JSONStorage implements Storage using JSON files.
type JSONStorage struct {
	fs       FileSystem
	filePath string
}

// NewJSONStorage creates a new JSON storage backend.
func NewJSONStorage(fs FileSystem, filePath string) *JSONStorage {
	return &JSONStorage{
		fs:       fs,
		filePath: filePath,
	}
}

// Load reads the registry from the JSON file.
func (s *JSONStorage) Load(_ context.Context) (RegistryData, error) {
	// Read the file
	data, err := s.fs.ReadFile(s.filePath)
	if err != nil {
		// Check if file doesn't exist using errors.Is to handle wrapped errors
		if errors.Is(err, os.ErrNotExist) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("read registry file: %w", err)
	}

	// Parse JSON
	var registry RegistryData
	if len(data) == 0 {
		// Empty file, return empty registry
		return make(RegistryData), nil
	}

	if unmarshalErr := json.Unmarshal(data, &registry); unmarshalErr != nil {
		return nil, fmt.Errorf("parse registry JSON: %w", unmarshalErr)
	}

	if registry == nil {
		registry = make(RegistryData)
	}

	return registry, nil
}

// Save atomically writes the registry to the JSON file.
func (s *JSONStorage) Save(_ context.Context, data RegistryData) error {
	// Ensure directory exists
	dir := filepath.Dir(s.filePath)
	const dirPerm = 0755
	if err := s.fs.MkdirAll(dir, FileMode(dirPerm)); err != nil {
		return fmt.Errorf("create registry directory: %w", err)
	}

	// Marshal to JSON with indentation for readability
	jsonData, err := json.MarshalIndent(data, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal registry JSON: %w", err)
	}

	// Add newline at end for better text file formatting
	jsonData = append(jsonData, '\n')

	// Write atomically by writing to temp file and moving
	tempFile := s.filePath + ".tmp"
	const filePerm = 0644
	if writeErr := s.fs.WriteFile(tempFile, jsonData, FileMode(filePerm)); writeErr != nil {
		return fmt.Errorf("write temp registry file: %w", writeErr)
	}

	// Rename temp file to actual file (atomic on most filesystems)
	if renameErr := os.Rename(tempFile, s.filePath); renameErr != nil {
		// Try to clean up temp file
		_ = os.Remove(tempFile)
		return fmt.Errorf("rename registry file: %w", renameErr)
	}

	return nil
}

// realFileSystem implements FileSystem using os package.
type realFileSystem struct{}

// newRealFileSystem creates a new real file system implementation.
func newRealFileSystem() *realFileSystem {
	return &realFileSystem{}
}

// ReadFile reads the entire contents of a file.
func (fs *realFileSystem) ReadFile(name string) ([]byte, error) {
	data, err := os.ReadFile(name) // #nosec G304 - file path is from trusted source
	if err != nil {
		return nil, fmt.Errorf("read file: %w", err)
	}
	return data, nil
}

// WriteFile writes data to a file atomically.
func (fs *realFileSystem) WriteFile(name string, data []byte, perm FileMode) error {
	if err := os.WriteFile(name, data, os.FileMode(perm)); err != nil {
		return fmt.Errorf("write file: %w", err)
	}
	return nil
}

// MkdirAll creates a directory and all necessary parents.
func (fs *realFileSystem) MkdirAll(path string, perm FileMode) error {
	if err := os.MkdirAll(path, os.FileMode(perm)); err != nil {
		return fmt.Errorf("mkdir all: %w", err)
	}
	return nil
}

// UserHomeDir returns the user's home directory.
func (fs *realFileSystem) UserHomeDir() (string, error) {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("get user home dir: %w", err)
	}
	return homeDir, nil
}

// DefaultStorage creates a storage instance with the default file path.
func DefaultStorage() *JSONStorage {
	return NewJSONStorage(newRealFileSystem(), getRegistryPath())
}
