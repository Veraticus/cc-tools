package shared

import (
	"fmt"
	"os"
	"path/filepath"
)

// FileSystem provides filesystem operations.
type FileSystem interface {
	Stat(name string) (os.FileInfo, error)
	Getwd() (string, error)
	Abs(path string) (string, error)
}

// Dependencies holds all external dependencies for the shared package.
type Dependencies struct {
	FS FileSystem
}

// Production implementation of FileSystem.
type realFileSystem struct{}

func (r *realFileSystem) Stat(name string) (os.FileInfo, error) {
	info, err := os.Stat(name)
	if err != nil {
		return nil, fmt.Errorf("stat %s: %w", name, err)
	}
	return info, nil
}

func (r *realFileSystem) Getwd() (string, error) {
	dir, err := os.Getwd()
	if err != nil {
		return "", fmt.Errorf("get working directory: %w", err)
	}
	return dir, nil
}

func (r *realFileSystem) Abs(path string) (string, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", fmt.Errorf("get absolute path %s: %w", path, err)
	}
	return abs, nil
}

// NewDefaultDependencies creates production dependencies.
func NewDefaultDependencies() *Dependencies {
	return &Dependencies{
		FS: &realFileSystem{},
	}
}
