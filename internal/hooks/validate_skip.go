package hooks

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"path/filepath"

	"github.com/Veraticus/cc-tools/internal/skipregistry"
)

// ValidateWithSkipCheck reads stdin, checks skip registry, and runs validation.
// This is the main entry point for both cc-tools validate and cc-tools-validate binaries.
func ValidateWithSkipCheck(
	ctx context.Context,
	stdin io.Reader,
	stdout io.Writer,
	stderr io.Writer,
	debug bool,
	timeoutSecs int,
	cooldownSecs int,
) int {
	// Read stdin once
	stdinData, err := io.ReadAll(stdin)
	if err != nil {
		// If we can't read input, run normally without skip checking
		return RunValidateHook(ctx, debug, timeoutSecs, cooldownSecs, nil)
	}

	// Check if directory should be skipped
	skipLint, skipTest := checkSkipsFromInput(ctx, stdinData, debug, stderr)

	// If both are skipped, exit silently
	if skipLint && skipTest {
		if debug {
			_, _ = fmt.Fprintf(stderr, "Both lint and test skipped, exiting silently\n")
		}
		return 0
	}

	// Pass skip information to the validate hook
	skipConfig := &SkipConfig{
		SkipLint: skipLint,
		SkipTest: skipTest,
	}

	// Create dependencies with our input reader
	deps := &Dependencies{
		Input:   &bytesInputReader{data: stdinData},
		Stdout:  stdout,
		Stderr:  stderr,
		FS:      NewDefaultDependencies().FS,
		Runner:  NewDefaultDependencies().Runner,
		Process: NewDefaultDependencies().Process,
		Clock:   NewDefaultDependencies().Clock,
	}

	return RunValidateHookWithSkip(ctx, debug, timeoutSecs, cooldownSecs, skipConfig, deps)
}

// bytesInputReader implements InputReader for a byte slice.
type bytesInputReader struct {
	data []byte
}

func (b *bytesInputReader) ReadAll() ([]byte, error) {
	return b.data, nil
}

func (b *bytesInputReader) IsTerminal() bool {
	return false
}

// checkSkipsFromInput parses the JSON input and checks the skip registry.
func checkSkipsFromInput(ctx context.Context, stdinData []byte, debug bool, stderr io.Writer) (bool, bool) {
	// Parse the JSON
	var input map[string]any
	if err := json.Unmarshal(stdinData, &input); err != nil {
		// If we can't decode input, don't skip
		if debug {
			_, _ = fmt.Fprintf(stderr, "Failed to parse JSON input: %v\n", err)
		}
		return false, false
	}

	// Get file path from input
	var filePath string
	if toolInput, ok := input["tool_input"].(map[string]any); ok {
		if fp, fpOk := toolInput["file_path"].(string); fpOk {
			filePath = fp
		}
	}

	if filePath == "" {
		// No file path, don't skip
		if debug {
			_, _ = fmt.Fprintf(stderr, "No file path found in input\n")
		}
		return false, false
	}

	// Get directory from file path
	dir := filepath.Dir(filePath)

	// Convert to absolute path
	absDir, err := filepath.Abs(dir)
	if err != nil {
		if debug {
			_, _ = fmt.Fprintf(stderr, "Failed to get absolute path: %v\n", err)
		}
		return false, false
	}

	// Check skip registry
	storage := skipregistry.DefaultStorage()
	registry := skipregistry.NewRegistry(storage)

	skipLint, _ := registry.IsSkipped(ctx, skipregistry.DirectoryPath(absDir), skipregistry.SkipTypeLint)
	skipTest, _ := registry.IsSkipped(ctx, skipregistry.DirectoryPath(absDir), skipregistry.SkipTypeTest)

	if debug {
		_, _ = fmt.Fprintf(stderr, "Checking skips for directory: %s\n", absDir)
		if skipLint {
			_, _ = fmt.Fprintf(stderr, "Skipping lint for directory: %s\n", absDir)
		}
		if skipTest {
			_, _ = fmt.Fprintf(stderr, "Skipping test for directory: %s\n", absDir)
		}
	}

	return skipLint, skipTest
}
