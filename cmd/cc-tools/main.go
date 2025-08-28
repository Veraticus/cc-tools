// Package main implements the cc-tools CLI application.
package main

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/Veraticus/cc-tools/internal/config"
	"github.com/Veraticus/cc-tools/internal/hooks"
	"github.com/Veraticus/cc-tools/internal/statusline"
)

const minArgs = 2

// Build-time variables.
var version = "dev"

func main() {
	// Debug logging - log all invocations to a file
	debugLog()

	if len(os.Args) < minArgs {
		printUsage()
		os.Exit(1)
	}

	switch os.Args[1] {
	case "statusline":
		runStatusline()
	case "lint":
		runLint()
	case "test":
		runTest()
	case "version":
		// Print version to stdout as intended output
		fmt.Printf("cc-tools %s\n", version) //nolint:forbidigo // CLI output
	case "help", "-h", "--help":
		printUsage()
	default:
		fmt.Fprintf(os.Stderr, "Unknown command: %s\n", os.Args[1])
		printUsage()
		os.Exit(1)
	}
}

func printUsage() {
	fmt.Fprintf(os.Stderr, `cc-tools - Claude Code Tools

Usage:
  cc-tools <command> [arguments]

Commands:
  statusline    Generate a status line for the prompt
  lint          Run smart linting
  test          Run smart testing
  version       Print version information
  help          Show this help message

Examples:
  echo '{"cwd": "/path"}' | cc-tools statusline
  echo '{"file_path": "main.go"}' | cc-tools lint
  echo '{"file_path": "main_test.go"}' | cc-tools test
`)
}

func runStatusline() {
	// Read stdin
	input, err := io.ReadAll(os.Stdin)
	if err != nil {
		// Fallback prompt output to stdout
		fmt.Print(" > ") //nolint:forbidigo // CLI output
		os.Exit(0)
	}

	// Recreate stdin reader
	reader := bytes.NewReader(input)

	result, err := runStatuslineWithInput(reader)
	if err != nil {
		// Fallback prompt output to stdout
		fmt.Print(" > ") //nolint:forbidigo // CLI output
		os.Exit(0)
	}
	// Output statusline result to stdout
	fmt.Print(result) //nolint:forbidigo // CLI output
}

func runLint() {
	// Load configuration for timeout values
	cfg, _ := config.Load()
	timeoutSecs := 30
	cooldownSecs := 2

	if cfg != nil {
		if cfg.Hooks.Lint.TimeoutSeconds > 0 {
			timeoutSecs = cfg.Hooks.Lint.TimeoutSeconds
		}
		if cfg.Hooks.Lint.CooldownSeconds > 0 {
			cooldownSecs = cfg.Hooks.Lint.CooldownSeconds
		}
	}

	debug := os.Getenv("CLAUDE_HOOKS_DEBUG") == "1"
	exitCode := hooks.RunSmartHook(context.Background(), hooks.CommandTypeLint, debug, timeoutSecs, cooldownSecs, nil)
	os.Exit(exitCode)
}

func runTest() {
	// Load configuration for timeout values
	cfg, _ := config.Load()
	timeoutSecs := 60
	cooldownSecs := 2

	if cfg != nil {
		if cfg.Hooks.Test.TimeoutSeconds > 0 {
			timeoutSecs = cfg.Hooks.Test.TimeoutSeconds
		}
		if cfg.Hooks.Test.CooldownSeconds > 0 {
			cooldownSecs = cfg.Hooks.Test.CooldownSeconds
		}
	}

	debug := os.Getenv("CLAUDE_HOOKS_DEBUG") == "1"
	exitCode := hooks.RunSmartHook(context.Background(), hooks.CommandTypeTest, debug, timeoutSecs, cooldownSecs, nil)
	os.Exit(exitCode)
}

func runStatuslineWithInput(reader io.Reader) (string, error) {
	deps := &statusline.Dependencies{
		FileReader:    &statusline.DefaultFileReader{},
		CommandRunner: &statusline.DefaultCommandRunner{},
		EnvReader:     &statusline.DefaultEnvReader{},
		TerminalWidth: &statusline.DefaultTerminalWidth{},
		CacheDir:      getCacheDir(),
		CacheDuration: getCacheDuration(),
	}

	sl := statusline.CreateStatusline(deps)

	result, err := sl.Generate(reader)
	if err != nil {
		return "", fmt.Errorf("generating statusline: %w", err)
	}

	return result, nil
}

func getCacheDir() string {
	if dir := os.Getenv("CLAUDE_STATUSLINE_CACHE_DIR"); dir != "" {
		return dir
	}
	return "/dev/shm"
}

func getCacheDuration() time.Duration {
	if os.Getenv("DEBUG_CONTEXT") == "1" {
		return 0
	}
	if seconds := os.Getenv("CLAUDE_STATUSLINE_CACHE_SECONDS"); seconds != "" {
		if duration, err := time.ParseDuration(seconds + "s"); err == nil {
			return duration
		}
	}
	const defaultCacheSeconds = 20
	return defaultCacheSeconds * time.Second
}

func debugLog() {
	// Create or append to debug log file
	debugFile := "/tmp/cc-tools.debug"
	f, err := os.OpenFile(debugFile, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0600)
	if err != nil {
		return // Silently fail if we can't write debug log
	}
	defer func() { _ = f.Close() }()

	// Read stdin and save it for both debug and actual use
	var stdinDebugData []byte
	if stat, _ := os.Stdin.Stat(); (stat.Mode() & os.ModeCharDevice) == 0 {
		// There's data in stdin
		stdinDebugData, _ = io.ReadAll(os.Stdin)
		// Create a new reader from the data we just read
		// This will be used by the actual commands
		newStdin, _ := os.Open("/dev/stdin") // Reset stdin
		os.Stdin = newStdin                  //nolint:reassign // Resetting stdin for subsequent reads
		// Actually, we need to pipe it back - create a temp file
		if tmpFile, tmpErr := os.CreateTemp("", "cc-tools-stdin-"); tmpErr == nil { //nolint:forbidigo // Debug temp file
			_, _ = tmpFile.Write(stdinDebugData)
			_, _ = tmpFile.Seek(0, 0)
			os.Stdin = tmpFile //nolint:reassign // Resetting stdin for subsequent reads
		}
	}

	// Log the invocation details
	timestamp := time.Now().Format("2006-01-02 15:04:05.000")
	_, _ = fmt.Fprintf(f, "\n========================================\n")
	_, _ = fmt.Fprintf(f, "[%s] cc-tools invoked\n", timestamp)
	_, _ = fmt.Fprintf(f, "Args: %v\n", os.Args)
	_, _ = fmt.Fprintf(f, "Environment:\n")
	_, _ = fmt.Fprintf(f, "  CLAUDE_HOOKS_DEBUG: %s\n", os.Getenv("CLAUDE_HOOKS_DEBUG"))
	_, _ = fmt.Fprintf(f, "  Working Dir: %s\n", func() string {
		if wd, wdErr := os.Getwd(); wdErr == nil {
			return wd
		}
		return "unknown"
	}())

	if len(stdinDebugData) > 0 {
		_, _ = fmt.Fprintf(f, "Stdin: %s\n", string(stdinDebugData))
	} else {
		_, _ = fmt.Fprintf(f, "Stdin: (no data available)\n")
	}

	_, _ = fmt.Fprintf(f, "Command: %s\n", func() string {
		if len(os.Args) > 1 {
			return os.Args[1]
		}
		return "(none)"
	}())
}
