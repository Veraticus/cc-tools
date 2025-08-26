package main

import (
	"fmt"
	"os"
	"time"

	"github.com/Veraticus/cc-tools/internal/statusline"
)

func main() {
	// Create dependencies with default implementations
	deps := &statusline.Dependencies{
		FileReader:    &statusline.DefaultFileReader{},
		CommandRunner: &statusline.DefaultCommandRunner{},
		EnvReader:     &statusline.DefaultEnvReader{},
		TerminalWidth: &statusline.DefaultTerminalWidth{},
		CacheDir:      getCacheDir(),
		CacheDuration: getCacheDuration(),
	}
	
	// Create statusline generator
	sl := statusline.New(deps)
	
	// Generate statusline from stdin
	result, err := sl.Generate(os.Stdin)
	if err != nil {
		// On error, output a simple fallback
		fmt.Print(" > ")
		os.Exit(0)
	}
	
	// Output without newline (statusline should fill exact width)
	fmt.Print(result)
}

func getCacheDir() string {
	if dir := os.Getenv("CLAUDE_STATUSLINE_CACHE_DIR"); dir != "" {
		return dir
	}
	return "/dev/shm"
}

func getCacheDuration() time.Duration {
	// Check for debug mode - disable cache
	if os.Getenv("DEBUG_CONTEXT") == "1" {
		return 0
	}
	if seconds := os.Getenv("CLAUDE_STATUSLINE_CACHE_SECONDS"); seconds != "" {
		if duration, err := time.ParseDuration(seconds + "s"); err == nil {
			return duration
		}
	}
	return 20 * time.Second
}
