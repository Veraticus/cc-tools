// Package main implements the steward-statusline CLI application.
package main

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/joshsymonds/steward/internal/aliases"
	"github.com/joshsymonds/steward/internal/output"
	"github.com/joshsymonds/steward/internal/statusline"
)

func main() {
	out := output.NewTerminal(os.Stdout, os.Stderr)

	// Read stdin
	input, err := io.ReadAll(os.Stdin)
	if err != nil {
		// Fallback prompt output to stdout
		out.Raw(" > ")
		os.Exit(0)
	}

	// Recreate stdin reader
	reader := bytes.NewReader(input)

	result, err := runStatuslineWithInput(reader)
	if err != nil {
		// Fallback prompt output to stdout
		out.Raw(" > ")
		os.Exit(0)
	}
	// Output statusline result to stdout
	out.Raw(result)
}

func runStatuslineWithInput(reader io.Reader) (string, error) {
	deps := &statusline.Dependencies{
		FileReader:    &statusline.DefaultFileReader{},
		CommandRunner: &statusline.DefaultCommandRunner{},
		EnvReader:     &statusline.DefaultEnvReader{},
		TerminalWidth: &statusline.DefaultTerminalWidth{},
		Resolver:      aliases.NewResolverFromDefaultPath(os.Stderr, "steward-statusline"),
		CacheDir:      statusline.ResolveCacheDir(),
		CacheDuration: getCacheDuration(),
	}

	sl := statusline.CreateStatusline(deps)

	result, err := sl.Generate(reader)
	if err != nil {
		return "", fmt.Errorf("generating statusline: %w", err)
	}

	return result, nil
}

func getCacheDuration() time.Duration {
	if os.Getenv("DEBUG_CONTEXT") == "1" {
		return 0
	}
	seconds := os.Getenv("STEWARD_STATUSLINE_CACHE_SECONDS")
	if seconds != "" {
		if duration, err := time.ParseDuration(seconds + "s"); err == nil {
			return duration
		}
	}
	const defaultCacheSeconds = 20
	return defaultCacheSeconds * time.Second
}
