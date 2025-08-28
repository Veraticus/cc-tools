package shared

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// Mock implementations for testing.
type mockFileSystem struct {
	statFunc  func(name string) (os.FileInfo, error)
	getwdFunc func() (string, error)
	absFunc   func(name string) (string, error)
}

func (m *mockFileSystem) Stat(name string) (os.FileInfo, error) {
	if m.statFunc != nil {
		return m.statFunc(name)
	}
	return nil, os.ErrNotExist
}

func (m *mockFileSystem) Getwd() (string, error) {
	if m.getwdFunc != nil {
		return m.getwdFunc()
	}
	return "/home/user/project", nil
}

func (m *mockFileSystem) Abs(path string) (string, error) {
	if m.absFunc != nil {
		return m.absFunc(path)
	}
	// Simple mock implementation
	if path == "" || path[0] != '/' {
		return filepath.Join("/home/user/project", path), nil
	}
	return path, nil
}

// Mock FileInfo implementation.
type mockFileInfo struct {
	name    string
	size    int64
	mode    os.FileMode
	modTime time.Time
	isDir   bool
}

func (m mockFileInfo) Name() string       { return m.name }
func (m mockFileInfo) Size() int64        { return m.size }
func (m mockFileInfo) Mode() os.FileMode  { return m.mode }
func (m mockFileInfo) ModTime() time.Time { return m.modTime }
func (m mockFileInfo) IsDir() bool        { return m.isDir }

func (m mockFileInfo) Sys() any { return nil }

func TestFindProjectRoot(t *testing.T) {
	tests := []struct {
		name      string
		startDir  string
		mockFS    *mockFileSystem
		expected  string
		expectErr bool
	}{
		{
			name:     "finds git project root",
			startDir: "/home/user/project/src/nested",
			mockFS: &mockFileSystem{
				statFunc: func(name string) (os.FileInfo, error) {
					if name == "/home/user/project/.git" {
						return mockFileInfo{name: ".git", isDir: true}, nil
					}
					return nil, os.ErrNotExist
				},
				absFunc: func(path string) (string, error) {
					return path, nil
				},
			},
			expected: "/home/user/project",
		},
		{
			name:     "finds go.mod project root",
			startDir: "/home/user/goproject/internal",
			mockFS: &mockFileSystem{
				statFunc: func(name string) (os.FileInfo, error) {
					if name == "/home/user/goproject/go.mod" {
						return mockFileInfo{name: "go.mod"}, nil
					}
					return nil, os.ErrNotExist
				},
				absFunc: func(path string) (string, error) {
					return path, nil
				},
			},
			expected: "/home/user/goproject",
		},
		{
			name:     "finds package.json project root",
			startDir: "/home/user/jsproject/src",
			mockFS: &mockFileSystem{
				statFunc: func(name string) (os.FileInfo, error) {
					if name == "/home/user/jsproject/package.json" {
						return mockFileInfo{name: "package.json"}, nil
					}
					return nil, os.ErrNotExist
				},
				absFunc: func(path string) (string, error) {
					return path, nil
				},
			},
			expected: "/home/user/jsproject",
		},
		{
			name:     "returns original dir when no root found",
			startDir: "/tmp/no-project",
			mockFS: &mockFileSystem{
				statFunc: func(_ string) (os.FileInfo, error) {
					return nil, os.ErrNotExist
				},
				absFunc: func(path string) (string, error) {
					return path, nil
				},
			},
			expected: "/tmp/no-project",
		},
		{
			name:     "uses working dir when startDir is empty",
			startDir: "",
			mockFS: &mockFileSystem{
				getwdFunc: func() (string, error) {
					return "/home/user/current", nil
				},
				statFunc: func(name string) (os.FileInfo, error) {
					if name == "/home/user/current/.git" {
						return mockFileInfo{name: ".git", isDir: true}, nil
					}
					return nil, os.ErrNotExist
				},
				absFunc: func(path string) (string, error) {
					if path == "" {
						return "/home/user/current", nil
					}
					return path, nil
				},
			},
			expected: "/home/user/current",
		},
		{
			name:     "error getting working directory",
			startDir: "",
			mockFS: &mockFileSystem{
				getwdFunc: func() (string, error) {
					return "", errors.New("permission denied")
				},
			},
			expectErr: true,
		},
		{
			name:     "error getting absolute path",
			startDir: "relative/path",
			mockFS: &mockFileSystem{
				absFunc: func(_ string) (string, error) {
					return "", errors.New("invalid path")
				},
			},
			expectErr: true,
		},
		{
			name:     "finds Makefile project root",
			startDir: "/home/user/makeproject/src",
			mockFS: &mockFileSystem{
				statFunc: func(name string) (os.FileInfo, error) {
					if name == "/home/user/makeproject/Makefile" {
						return mockFileInfo{name: "Makefile"}, nil
					}
					return nil, os.ErrNotExist
				},
				absFunc: func(path string) (string, error) {
					return path, nil
				},
			},
			expected: "/home/user/makeproject",
		},
		{
			name:     "finds justfile project root (lowercase)",
			startDir: "/home/user/justproject/src",
			mockFS: &mockFileSystem{
				statFunc: func(name string) (os.FileInfo, error) {
					if name == "/home/user/justproject/justfile" {
						return mockFileInfo{name: "justfile"}, nil
					}
					return nil, os.ErrNotExist
				},
				absFunc: func(path string) (string, error) {
					return path, nil
				},
			},
			expected: "/home/user/justproject",
		},
		{
			name:     "finds Cargo.toml project root",
			startDir: "/home/user/rustproject/src",
			mockFS: &mockFileSystem{
				statFunc: func(name string) (os.FileInfo, error) {
					if name == "/home/user/rustproject/Cargo.toml" {
						return mockFileInfo{name: "Cargo.toml"}, nil
					}
					return nil, os.ErrNotExist
				},
				absFunc: func(path string) (string, error) {
					return path, nil
				},
			},
			expected: "/home/user/rustproject",
		},
		{
			name:     "finds pyproject.toml project root",
			startDir: "/home/user/pythonproject/lib",
			mockFS: &mockFileSystem{
				statFunc: func(name string) (os.FileInfo, error) {
					if name == "/home/user/pythonproject/pyproject.toml" {
						return mockFileInfo{name: "pyproject.toml"}, nil
					}
					return nil, os.ErrNotExist
				},
				absFunc: func(path string) (string, error) {
					return path, nil
				},
			},
			expected: "/home/user/pythonproject",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			deps := &Dependencies{FS: tt.mockFS}
			result, err := FindProjectRoot(tt.startDir, deps)

			if tt.expectErr {
				if err == nil {
					t.Errorf("expected error but got none")
				}
				return
			}

			if err != nil {
				t.Errorf("unexpected error: %v", err)
				return
			}

			if result != tt.expected {
				t.Errorf("expected %q, got %q", tt.expected, result)
			}
		})
	}
}

func TestDetectProjectType(t *testing.T) {
	tests := []struct {
		name       string
		projectDir string
		mockFS     *mockFileSystem
		expected   []string
	}{
		{
			name:       "go project with go.mod",
			projectDir: "/project",
			mockFS: &mockFileSystem{
				statFunc: func(name string) (os.FileInfo, error) {
					if name == "/project/go.mod" {
						return mockFileInfo{name: "go.mod"}, nil
					}
					return nil, os.ErrNotExist
				},
			},
			expected: []string{"go"},
		},
		{
			name:       "go project with go.sum",
			projectDir: "/project",
			mockFS: &mockFileSystem{
				statFunc: func(name string) (os.FileInfo, error) {
					if name == "/project/go.sum" {
						return mockFileInfo{name: "go.sum"}, nil
					}
					return nil, os.ErrNotExist
				},
			},
			expected: []string{"go"},
		},
		{
			name:       "python project with pyproject.toml",
			projectDir: "/project",
			mockFS: &mockFileSystem{
				statFunc: func(name string) (os.FileInfo, error) {
					if name == "/project/pyproject.toml" {
						return mockFileInfo{name: "pyproject.toml"}, nil
					}
					return nil, os.ErrNotExist
				},
			},
			expected: []string{"python"},
		},
		{
			name:       "python project with setup.py",
			projectDir: "/project",
			mockFS: &mockFileSystem{
				statFunc: func(name string) (os.FileInfo, error) {
					if name == "/project/setup.py" {
						return mockFileInfo{name: "setup.py"}, nil
					}
					return nil, os.ErrNotExist
				},
			},
			expected: []string{"python"},
		},
		{
			name:       "python project with requirements.txt",
			projectDir: "/project",
			mockFS: &mockFileSystem{
				statFunc: func(name string) (os.FileInfo, error) {
					if name == "/project/requirements.txt" {
						return mockFileInfo{name: "requirements.txt"}, nil
					}
					return nil, os.ErrNotExist
				},
			},
			expected: []string{"python"},
		},
		{
			name:       "javascript project with package.json",
			projectDir: "/project",
			mockFS: &mockFileSystem{
				statFunc: func(name string) (os.FileInfo, error) {
					if name == "/project/package.json" {
						return mockFileInfo{name: "package.json"}, nil
					}
					return nil, os.ErrNotExist
				},
			},
			expected: []string{"javascript"},
		},
		{
			name:       "typescript project with tsconfig.json",
			projectDir: "/project",
			mockFS: &mockFileSystem{
				statFunc: func(name string) (os.FileInfo, error) {
					if name == "/project/tsconfig.json" {
						return mockFileInfo{name: "tsconfig.json"}, nil
					}
					return nil, os.ErrNotExist
				},
			},
			expected: []string{"javascript"},
		},
		{
			name:       "rust project",
			projectDir: "/project",
			mockFS: &mockFileSystem{
				statFunc: func(name string) (os.FileInfo, error) {
					if name == "/project/Cargo.toml" {
						return mockFileInfo{name: "Cargo.toml"}, nil
					}
					return nil, os.ErrNotExist
				},
			},
			expected: []string{"rust"},
		},
		{
			name:       "nix project with flake.nix",
			projectDir: "/project",
			mockFS: &mockFileSystem{
				statFunc: func(name string) (os.FileInfo, error) {
					if name == "/project/flake.nix" {
						return mockFileInfo{name: "flake.nix"}, nil
					}
					return nil, os.ErrNotExist
				},
			},
			expected: []string{"nix"},
		},
		{
			name:       "nix project with default.nix",
			projectDir: "/project",
			mockFS: &mockFileSystem{
				statFunc: func(name string) (os.FileInfo, error) {
					if name == "/project/default.nix" {
						return mockFileInfo{name: "default.nix"}, nil
					}
					return nil, os.ErrNotExist
				},
			},
			expected: []string{"nix"},
		},
		{
			name:       "nix project with shell.nix",
			projectDir: "/project",
			mockFS: &mockFileSystem{
				statFunc: func(name string) (os.FileInfo, error) {
					if name == "/project/shell.nix" {
						return mockFileInfo{name: "shell.nix"}, nil
					}
					return nil, os.ErrNotExist
				},
			},
			expected: []string{"nix"},
		},
		{
			name:       "multi-language project",
			projectDir: "/project",
			mockFS: &mockFileSystem{
				statFunc: func(name string) (os.FileInfo, error) {
					switch name {
					case "/project/go.mod":
						return mockFileInfo{name: "go.mod"}, nil
					case "/project/package.json":
						return mockFileInfo{name: "package.json"}, nil
					case "/project/requirements.txt":
						return mockFileInfo{name: "requirements.txt"}, nil
					}
					return nil, os.ErrNotExist
				},
			},
			expected: []string{"go", "python", "javascript"},
		},
		{
			name:       "unknown project type",
			projectDir: "/project",
			mockFS: &mockFileSystem{
				statFunc: func(_ string) (os.FileInfo, error) {
					return nil, os.ErrNotExist
				},
			},
			expected: []string{"unknown"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			deps := &Dependencies{FS: tt.mockFS}
			result := DetectProjectType(tt.projectDir, deps)

			if len(result) != len(tt.expected) {
				t.Errorf("expected %v, got %v", tt.expected, result)
				return
			}

			for i := range result {
				if result[i] != tt.expected[i] {
					t.Errorf("expected %v, got %v", tt.expected, result)
					return
				}
			}
		})
	}
}

func TestGetPackageManager(t *testing.T) {
	tests := []struct {
		name       string
		projectDir string
		mockFS     *mockFileSystem
		expected   string
	}{
		{
			name:       "yarn project",
			projectDir: "/project",
			mockFS: &mockFileSystem{
				statFunc: func(name string) (os.FileInfo, error) {
					if name == "/project/yarn.lock" {
						return mockFileInfo{name: "yarn.lock"}, nil
					}
					return nil, os.ErrNotExist
				},
			},
			expected: "yarn",
		},
		{
			name:       "pnpm project",
			projectDir: "/project",
			mockFS: &mockFileSystem{
				statFunc: func(name string) (os.FileInfo, error) {
					if name == "/project/pnpm-lock.yaml" {
						return mockFileInfo{name: "pnpm-lock.yaml"}, nil
					}
					return nil, os.ErrNotExist
				},
			},
			expected: "pnpm",
		},
		{
			name:       "bun project",
			projectDir: "/project",
			mockFS: &mockFileSystem{
				statFunc: func(name string) (os.FileInfo, error) {
					if name == "/project/bun.lockb" {
						return mockFileInfo{name: "bun.lockb"}, nil
					}
					return nil, os.ErrNotExist
				},
			},
			expected: "bun",
		},
		{
			name:       "npm project (default)",
			projectDir: "/project",
			mockFS: &mockFileSystem{
				statFunc: func(_ string) (os.FileInfo, error) {
					return nil, os.ErrNotExist
				},
			},
			expected: "npm",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			deps := &Dependencies{FS: tt.mockFS}
			result := GetPackageManager(tt.projectDir, deps)

			if result != tt.expected {
				t.Errorf("expected %q, got %q", tt.expected, result)
			}
		})
	}
}

func TestShouldSkipFile(t *testing.T) {
	tests := []struct {
		name     string
		filePath string
		expected bool
	}{
		// Skip patterns
		{"vendor directory", "/project/vendor/github.com/lib/file.go", true},
		{"node_modules", "/project/node_modules/package/index.js", true},
		{"build directory", "/project/build/output.js", true},
		{"git directory", "/project/.git/config", true},
		{"dist directory", "/project/dist/bundle.js", true},
		{"python cache", "/project/__pycache__/module.pyc", true},
		{"cache directory", "/project/.cache/data", true},
		{"rust target", "/project/target/release/binary", true},
		{"next.js cache", "/project/.next/static/chunks/main.js", true},

		// Test files
		{"go test file", "/project/main_test.go", true},
		{"python test file", "/project/test_module_test.py", true},
		{"js test file", "/project/component.test.js", true},
		{"ts test file", "/project/component.test.ts", true},
		{"js spec file", "/project/component.spec.js", true},
		{"ts spec file", "/project/component.spec.ts", true},
		{"jsx test file", "/project/component.test.jsx", true},
		{"tsx test file", "/project/component.test.tsx", true},
		{"jsx spec file", "/project/component.spec.jsx", true},
		{"tsx spec file", "/project/component.spec.tsx", true},

		// Generated files
		{"generated go file", "/project/api.generated.go", true},
		{"protobuf go file", "/project/service.pb.go", true},
		{"gen go file", "/project/models.gen.go", true},
		{"underscore gen go file", "/project/types_gen.go", true},

		// Should not skip
		{"regular go file", "/project/main.go", false},
		{"regular js file", "/project/index.js", false},
		{"regular py file", "/project/module.py", false},
		{"vendored but not in vendor dir", "/project/vendored_lib.go", false},
		{"test in name but not suffix", "/project/test_utils.go", false},
		{"generated in name but not suffix", "/project/generated_utils.go", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := ShouldSkipFile(tt.filePath)
			if result != tt.expected {
				t.Errorf("ShouldSkipFile(%q) = %v, expected %v", tt.filePath, result, tt.expected)
			}
		})
	}
}

func TestProjectHelper(t *testing.T) {
	t.Run("NewProjectHelper with nil deps", func(t *testing.T) {
		helper := NewProjectHelper(nil)
		if helper == nil {
			t.Fatal("expected non-nil helper")
		}
		if helper.deps == nil {
			t.Error("expected non-nil deps")
		}
	})

	t.Run("NewProjectHelper with deps", func(t *testing.T) {
		deps := &Dependencies{FS: &mockFileSystem{}}
		helper := NewProjectHelper(deps)
		if helper == nil {
			t.Fatal("expected non-nil helper")
		}
		if helper.deps != deps {
			t.Error("expected same deps")
		}
	})
}

func TestFileExistsWithDeps(t *testing.T) {
	tests := []struct {
		name     string
		path     string
		mockFS   *mockFileSystem
		expected bool
	}{
		{
			name: "file exists",
			path: "/project/file.txt",
			mockFS: &mockFileSystem{
				statFunc: func(name string) (os.FileInfo, error) {
					if name == "/project/file.txt" {
						return mockFileInfo{name: "file.txt"}, nil
					}
					return nil, os.ErrNotExist
				},
			},
			expected: true,
		},
		{
			name: "file does not exist",
			path: "/project/missing.txt",
			mockFS: &mockFileSystem{
				statFunc: func(_ string) (os.FileInfo, error) {
					return nil, os.ErrNotExist
				},
			},
			expected: false,
		},
		{
			name: "permission error treated as not exists",
			path: "/project/forbidden.txt",
			mockFS: &mockFileSystem{
				statFunc: func(_ string) (os.FileInfo, error) {
					return nil, os.ErrPermission
				},
			},
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			deps := &Dependencies{FS: tt.mockFS}
			result := fileExists(tt.path, deps)
			if result != tt.expected {
				t.Errorf("expected %v, got %v", tt.expected, result)
			}
		})
	}
}

func TestFileExists(t *testing.T) {
	// Test convenience wrapper - just ensure it doesn't panic
	result := fileExists("/probably/nonexistent/file.txt", nil)
	if result {
		t.Log("Unexpectedly found file /probably/nonexistent/file.txt")
	}
}

func TestNewDefaultDependencies(t *testing.T) {
	deps := NewDefaultDependencies()
	if deps == nil {
		t.Fatal("expected non-nil dependencies")
	}
	if deps.FS == nil {
		t.Fatal("expected non-nil filesystem")
	}

	// Test that the real filesystem implementation works
	_, err := deps.FS.Getwd()
	if err != nil {
		t.Errorf("Getwd failed: %v", err)
	}
}
