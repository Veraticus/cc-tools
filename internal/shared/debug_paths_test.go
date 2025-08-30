package shared

import (
	"strings"
	"testing"
)

func TestGetDebugLogPathForDir(t *testing.T) {
	tests := []struct {
		name         string
		dir          string
		wantContains []string
		wantPrefix   string
		wantSuffix   string
	}{
		{
			name:         "normal directory path",
			dir:          "/home/user/projects/myapp",
			wantContains: []string{"projects-myapp"},
			wantPrefix:   "/tmp/cc-tools-",
			wantSuffix:   ".debug",
		},
		{
			name:         "single directory",
			dir:          "/tmp",
			wantContains: []string{"-tmp-"},
			wantPrefix:   "/tmp/cc-tools-",
			wantSuffix:   ".debug",
		},
		{
			name:         "deep nested path",
			dir:          "/very/deep/nested/directory/structure/here",
			wantContains: []string{"structure-here"},
			wantPrefix:   "/tmp/cc-tools-",
			wantSuffix:   ".debug",
		},
		{
			name:         "path with spaces",
			dir:          "/home/user/my projects/app name",
			wantContains: []string{"my_projects-app_name"},
			wantPrefix:   "/tmp/cc-tools-",
			wantSuffix:   ".debug",
		},
		{
			name:         "root directory",
			dir:          "/",
			wantContains: []string{"root"},
			wantPrefix:   "/tmp/cc-tools-",
			wantSuffix:   ".debug",
		},
		{
			name:         "empty parts after root",
			dir:          "//",
			wantContains: []string{"root"},
			wantPrefix:   "/tmp/cc-tools-",
			wantSuffix:   ".debug",
		},
		{
			name:         "trailing slash",
			dir:          "/home/user/project/",
			wantContains: []string{"user-project"},
			wantPrefix:   "/tmp/cc-tools-",
			wantSuffix:   ".debug",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := GetDebugLogPathForDir(tt.dir)

			// Check prefix
			if !strings.HasPrefix(got, tt.wantPrefix) {
				t.Errorf("GetDebugLogPathForDir() = %v, want prefix %v", got, tt.wantPrefix)
			}

			// Check suffix
			if !strings.HasSuffix(got, tt.wantSuffix) {
				t.Errorf("GetDebugLogPathForDir() = %v, want suffix %v", got, tt.wantSuffix)
			}

			// Check contains expected parts
			for _, want := range tt.wantContains {
				if !strings.Contains(got, want) {
					t.Errorf("GetDebugLogPathForDir() = %v, want to contain %v", got, want)
				}
			}

			// Check that it has a hash component (8 hex chars before .debug)
			parts := strings.Split(got, "-")
			if len(parts) < 3 {
				t.Errorf("GetDebugLogPathForDir() = %v, expected at least 3 parts separated by '-'", got)
			}

			lastPart := parts[len(parts)-1]
			hashPart := strings.TrimSuffix(lastPart, ".debug")
			if len(hashPart) != 8 { // 4 bytes = 8 hex chars
				t.Errorf("GetDebugLogPathForDir() hash part = %v, expected 8 hex characters", hashPart)
			}

			// Verify it's valid hex
			for _, ch := range hashPart {
				if (ch < '0' || ch > '9') && (ch < 'a' || ch > 'f') {
					t.Errorf("GetDebugLogPathForDir() hash part = %v, contains non-hex character %c", hashPart, ch)
				}
			}
		})
	}
}

func TestGetDebugLogPathForDirConsistency(t *testing.T) {
	// Test that the same directory always produces the same path
	dir := "/home/user/test/project"

	path1 := GetDebugLogPathForDir(dir)
	path2 := GetDebugLogPathForDir(dir)
	path3 := GetDebugLogPathForDir(dir)

	if path1 != path2 || path2 != path3 {
		t.Errorf("GetDebugLogPathForDir() returned different paths for same directory: %v, %v, %v", path1, path2, path3)
	}

	// Test that different directories produce different paths
	dir2 := "/home/user/test/other"
	path4 := GetDebugLogPathForDir(dir2)

	if path4 == path1 {
		t.Errorf("GetDebugLogPathForDir() returned same path for different directories")
	}
}

func TestGetDebugLogPathForDirEdgeCases(t *testing.T) {
	// Test with relative path (should still work)
	path := GetDebugLogPathForDir("relative/path")
	if !strings.HasPrefix(path, "/tmp/cc-tools-") || !strings.HasSuffix(path, ".debug") {
		t.Errorf("GetDebugLogPathForDir() with relative path = %v, want /tmp/cc-tools-*.debug", path)
	}

	// Test with path containing multiple slashes
	path2 := GetDebugLogPathForDir("/home//user///project")
	if !strings.Contains(path2, "user-project") {
		t.Errorf("GetDebugLogPathForDir() didn't handle multiple slashes correctly: %v", path2)
	}

	// Test with very long directory name
	longName := strings.Repeat("a", 100) + "/" + strings.Repeat("b", 100)
	path3 := GetDebugLogPathForDir(longName)
	if !strings.HasPrefix(path3, "/tmp/cc-tools-") || !strings.HasSuffix(path3, ".debug") {
		t.Errorf("GetDebugLogPathForDir() with long name = %v, want /tmp/cc-tools-*.debug", path3)
	}
}
