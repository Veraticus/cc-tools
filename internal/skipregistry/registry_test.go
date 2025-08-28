package skipregistry

import (
	"context"
	"errors"
	"testing"
)

// mockStorage is a mock implementation of Storage for testing.
type mockStorage struct {
	data      RegistryData
	loadErr   error
	saveErr   error
	loadCalls int
	saveCalls int
}

func newMockStorage() *mockStorage {
	return &mockStorage{
		data: make(RegistryData),
	}
}

func (m *mockStorage) Load(_ context.Context) (RegistryData, error) {
	m.loadCalls++
	if m.loadErr != nil {
		return nil, m.loadErr
	}
	// Return a copy to prevent mutations
	dataCopy := make(RegistryData)
	for k, v := range m.data {
		types := make([]string, 0, len(v))
		dataCopy[k] = append(types, v...)
	}
	return dataCopy, nil
}

func (m *mockStorage) Save(_ context.Context, data RegistryData) error {
	m.saveCalls++
	if m.saveErr != nil {
		return m.saveErr
	}
	// Save a copy to prevent mutations
	m.data = make(RegistryData)
	for k, v := range data {
		types := make([]string, 0, len(v))
		m.data[k] = append(types, v...)
	}
	return nil
}

func TestRegistry_IsSkipped(t *testing.T) {
	tests := []struct {
		name      string
		setupData RegistryData
		dir       DirectoryPath
		skipType  SkipType
		want      bool
		wantErr   bool
	}{
		{
			name:      "empty registry returns false",
			setupData: RegistryData{},
			dir:       "/project",
			skipType:  SkipTypeLint,
			want:      false,
			wantErr:   false,
		},
		{
			name: "finds lint skip",
			setupData: RegistryData{
				"/project": {"lint"},
			},
			dir:      "/project",
			skipType: SkipTypeLint,
			want:     true,
			wantErr:  false,
		},
		{
			name: "finds test skip",
			setupData: RegistryData{
				"/project": {"test"},
			},
			dir:      "/project",
			skipType: SkipTypeTest,
			want:     true,
			wantErr:  false,
		},
		{
			name: "multiple skip types",
			setupData: RegistryData{
				"/project": {"lint", "test"},
			},
			dir:      "/project",
			skipType: SkipTypeLint,
			want:     true,
			wantErr:  false,
		},
		{
			name: "different directory not skipped",
			setupData: RegistryData{
				"/project": {"lint"},
			},
			dir:      "/other",
			skipType: SkipTypeLint,
			want:     false,
			wantErr:  false,
		},
		{
			name:      "invalid path returns error",
			setupData: RegistryData{},
			dir:       "relative/path",
			skipType:  SkipTypeLint,
			want:      false,
			wantErr:   true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			storage := newMockStorage()
			storage.data = tt.setupData
			r := NewRegistry(storage)

			got, err := r.IsSkipped(context.Background(), tt.dir, tt.skipType)
			if (err != nil) != tt.wantErr {
				t.Errorf("IsSkipped() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if got != tt.want {
				t.Errorf("IsSkipped() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestRegistry_AddSkip(t *testing.T) {
	tests := []struct {
		name      string
		setupData RegistryData
		dir       DirectoryPath
		skipType  SkipType
		wantData  RegistryData
		wantErr   bool
	}{
		{
			name:      "add lint to empty registry",
			setupData: RegistryData{},
			dir:       "/project",
			skipType:  SkipTypeLint,
			wantData: RegistryData{
				"/project": {"lint"},
			},
			wantErr: false,
		},
		{
			name: "add test to existing lint",
			setupData: RegistryData{
				"/project": {"lint"},
			},
			dir:      "/project",
			skipType: SkipTypeTest,
			wantData: RegistryData{
				"/project": {"lint", "test"},
			},
			wantErr: false,
		},
		{
			name:      "add all expands to both",
			setupData: RegistryData{},
			dir:       "/project",
			skipType:  SkipTypeAll,
			wantData: RegistryData{
				"/project": {"lint", "test"},
			},
			wantErr: false,
		},
		{
			name: "add duplicate is idempotent",
			setupData: RegistryData{
				"/project": {"lint"},
			},
			dir:      "/project",
			skipType: SkipTypeLint,
			wantData: RegistryData{
				"/project": {"lint"},
			},
			wantErr: false,
		},
		{
			name:      "invalid path returns error",
			setupData: RegistryData{},
			dir:       "relative/path",
			skipType:  SkipTypeLint,
			wantData:  RegistryData{},
			wantErr:   true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			storage := newMockStorage()
			storage.data = tt.setupData
			r := NewRegistry(storage)

			err := r.AddSkip(context.Background(), tt.dir, tt.skipType)
			if (err != nil) != tt.wantErr {
				t.Errorf("AddSkip() error = %v, wantErr %v", err, tt.wantErr)
				return
			}

			if !tt.wantErr {
				// Verify the data was saved correctly
				if len(storage.data) != len(tt.wantData) {
					t.Errorf("AddSkip() data length = %v, want %v", len(storage.data), len(tt.wantData))
				}
				for path, types := range tt.wantData {
					gotTypes := storage.data[path]
					if len(gotTypes) != len(types) {
						t.Errorf("AddSkip() types for %s = %v, want %v", path, gotTypes, types)
					}
					for i, wantType := range types {
						if i >= len(gotTypes) || gotTypes[i] != wantType {
							t.Errorf("AddSkip() type[%d] for %s = %v, want %v", i, path, gotTypes[i], wantType)
						}
					}
				}
			}
		})
	}
}

func TestRegistry_RemoveSkip(t *testing.T) {
	tests := []struct {
		name      string
		setupData RegistryData
		dir       DirectoryPath
		skipType  SkipType
		wantData  RegistryData
		wantErr   bool
	}{
		{
			name: "remove lint keeps test",
			setupData: RegistryData{
				"/project": {"lint", "test"},
			},
			dir:      "/project",
			skipType: SkipTypeLint,
			wantData: RegistryData{
				"/project": {"test"},
			},
			wantErr: false,
		},
		{
			name: "remove last type removes entry",
			setupData: RegistryData{
				"/project": {"lint"},
			},
			dir:      "/project",
			skipType: SkipTypeLint,
			wantData: RegistryData{},
			wantErr:  false,
		},
		{
			name: "remove all removes both",
			setupData: RegistryData{
				"/project": {"lint", "test"},
			},
			dir:      "/project",
			skipType: SkipTypeAll,
			wantData: RegistryData{},
			wantErr:  false,
		},
		{
			name: "remove from non-existent is idempotent",
			setupData: RegistryData{
				"/other": {"lint"},
			},
			dir:      "/project",
			skipType: SkipTypeLint,
			wantData: RegistryData{
				"/other": {"lint"},
			},
			wantErr: false,
		},
		{
			name:      "invalid path returns error",
			setupData: RegistryData{},
			dir:       "relative/path",
			skipType:  SkipTypeLint,
			wantData:  RegistryData{},
			wantErr:   true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			storage := newMockStorage()
			storage.data = tt.setupData
			r := NewRegistry(storage)

			err := r.RemoveSkip(context.Background(), tt.dir, tt.skipType)
			if (err != nil) != tt.wantErr {
				t.Errorf("RemoveSkip() error = %v, wantErr %v", err, tt.wantErr)
				return
			}

			if !tt.wantErr {
				// Verify the data was saved correctly
				if len(storage.data) != len(tt.wantData) {
					t.Errorf("RemoveSkip() data length = %v, want %v", len(storage.data), len(tt.wantData))
				}
				for path, types := range tt.wantData {
					gotTypes := storage.data[path]
					if len(gotTypes) != len(types) {
						t.Errorf("RemoveSkip() types for %s = %v, want %v", path, gotTypes, types)
					}
					for i, wantType := range types {
						if i >= len(gotTypes) || gotTypes[i] != wantType {
							t.Errorf("RemoveSkip() type[%d] for %s = %v, want %v", i, path, gotTypes[i], wantType)
						}
					}
				}
			}
		})
	}
}

func TestRegistry_Clear(t *testing.T) {
	tests := []struct {
		name      string
		setupData RegistryData
		dir       DirectoryPath
		wantData  RegistryData
		wantErr   bool
	}{
		{
			name: "clear removes all types",
			setupData: RegistryData{
				"/project": {"lint", "test"},
				"/other":   {"lint"},
			},
			dir: "/project",
			wantData: RegistryData{
				"/other": {"lint"},
			},
			wantErr: false,
		},
		{
			name: "clear non-existent is idempotent",
			setupData: RegistryData{
				"/other": {"lint"},
			},
			dir: "/project",
			wantData: RegistryData{
				"/other": {"lint"},
			},
			wantErr: false,
		},
		{
			name:      "invalid path returns error",
			setupData: RegistryData{},
			dir:       "relative/path",
			wantData:  RegistryData{},
			wantErr:   true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			storage := newMockStorage()
			storage.data = tt.setupData
			r := NewRegistry(storage)

			err := r.Clear(context.Background(), tt.dir)
			if (err != nil) != tt.wantErr {
				t.Errorf("Clear() error = %v, wantErr %v", err, tt.wantErr)
				return
			}

			if !tt.wantErr {
				// Verify the data was saved correctly
				if len(storage.data) != len(tt.wantData) {
					t.Errorf("Clear() data length = %v, want %v", len(storage.data), len(tt.wantData))
				}
				for path, types := range tt.wantData {
					gotTypes := storage.data[path]
					if len(gotTypes) != len(types) {
						t.Errorf("Clear() types for %s = %v, want %v", path, gotTypes, types)
					}
				}
			}
		})
	}
}

func TestRegistry_ListAll(t *testing.T) {
	tests := []struct {
		name      string
		setupData RegistryData
		wantCount int
		wantErr   bool
	}{
		{
			name:      "empty registry returns empty list",
			setupData: RegistryData{},
			wantCount: 0,
			wantErr:   false,
		},
		{
			name: "returns all entries",
			setupData: RegistryData{
				"/project1": {"lint"},
				"/project2": {"test"},
				"/project3": {"lint", "test"},
			},
			wantCount: 3,
			wantErr:   false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			storage := newMockStorage()
			storage.data = tt.setupData
			r := NewRegistry(storage)

			got, err := r.ListAll(context.Background())
			if (err != nil) != tt.wantErr {
				t.Errorf("ListAll() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if len(got) != tt.wantCount {
				t.Errorf("ListAll() returned %v entries, want %v", len(got), tt.wantCount)
			}
		})
	}
}

func TestRegistry_SaveError(t *testing.T) {
	// Test that save errors are handled properly
	storage := newMockStorage()
	storage.saveErr = errors.New("save failed")
	r := NewRegistry(storage)

	// Try to add a skip
	err := r.AddSkip(context.Background(), "/project", SkipTypeLint)
	if err == nil {
		t.Errorf("AddSkip() should have returned error when save fails")
	}

	// Verify the cache was reverted
	isSkipped, _ := r.IsSkipped(context.Background(), "/project", SkipTypeLint)
	if isSkipped {
		t.Errorf("Cache should have been reverted after save failure")
	}
}

func TestRegistry_LoadOnce(t *testing.T) {
	// Test that Load is only called once
	storage := newMockStorage()
	storage.data = RegistryData{
		"/project": {"lint"},
	}
	r := NewRegistry(storage)

	// First call should load
	_, _ = r.IsSkipped(context.Background(), "/project", SkipTypeLint)
	if storage.loadCalls != 1 {
		t.Errorf("Expected 1 load call, got %d", storage.loadCalls)
	}

	// Second call should not load again
	_, _ = r.IsSkipped(context.Background(), "/project", SkipTypeTest)
	if storage.loadCalls != 1 {
		t.Errorf("Expected 1 load call after second operation, got %d", storage.loadCalls)
	}
}

func TestParseSkipType(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		want    SkipType
		wantErr bool
	}{
		{
			name:    "parse lint",
			input:   "lint",
			want:    SkipTypeLint,
			wantErr: false,
		},
		{
			name:    "parse test",
			input:   "test",
			want:    SkipTypeTest,
			wantErr: false,
		},
		{
			name:    "parse all",
			input:   "all",
			want:    SkipTypeAll,
			wantErr: false,
		},
		{
			name:    "invalid type",
			input:   "invalid",
			want:    "",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParseSkipType(tt.input)
			if (err != nil) != tt.wantErr {
				t.Errorf("ParseSkipType() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if got != tt.want {
				t.Errorf("ParseSkipType() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestDirectoryPath_Validate(t *testing.T) {
	tests := []struct {
		name    string
		path    DirectoryPath
		wantErr bool
	}{
		{
			name:    "absolute path is valid",
			path:    "/project",
			wantErr: false,
		},
		{
			name:    "relative path is invalid",
			path:    "project",
			wantErr: true,
		},
		{
			name:    "relative path with dots is invalid",
			path:    "./project",
			wantErr: true,
		},
		{
			name:    "empty path is invalid",
			path:    "",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.path.Validate()
			if (err != nil) != tt.wantErr {
				t.Errorf("DirectoryPath.Validate() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}
