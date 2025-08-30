// Package main implements the cc-tools CLI application.
package main

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"strconv"
	"time"

	"github.com/Veraticus/cc-tools/internal/config"
	"github.com/Veraticus/cc-tools/internal/hooks"
	"github.com/Veraticus/cc-tools/internal/output"
	"github.com/Veraticus/cc-tools/internal/shared"
	"github.com/Veraticus/cc-tools/internal/statusline"
)

const (
	minArgs     = 2
	helpFlag    = "--help"
	helpCommand = "help"
)

// Build-time variables.
var version = "dev"

func main() {
	out := output.NewTerminal(os.Stdout, os.Stderr)

	// Debug logging - log all invocations to a file
	debugLog()

	if len(os.Args) < minArgs {
		printUsage(out)
		os.Exit(1)
	}

	switch os.Args[1] {
	case "statusline":
		runStatusline()
	case "validate":
		runValidate()
	case "skip":
		runSkipCommand()
	case "unskip":
		runUnskipCommand()
	case "debug":
		runDebugCommand()
	case "mcp":
		runMCPCommand()
	case "version":
		// Print version to stdout as intended output
		out.Raw(fmt.Sprintf("cc-tools %s\n", version))
	case helpCommand, "-h", helpFlag:
		printUsage(out)
	default:
		out.Error("Unknown command: %s", os.Args[1])
		printUsage(out)
		os.Exit(1)
	}
}

func printUsage(out *output.Terminal) {
	out.RawError(`cc-tools - Claude Code Tools

Usage:
  cc-tools <command> [arguments]

Commands:
  statusline    Generate a status line for the prompt
  validate      Run smart validation (lint and test in parallel)
  skip          Configure skip settings for directories
  unskip        Remove skip settings from directories
  debug         Configure debug logging for directories
  mcp           Manage Claude MCP servers
  version       Print version information
  help          Show this help message

Examples:
  echo '{"cwd": "/path"}' | cc-tools statusline
  echo '{"file_path": "main.go"}' | cc-tools validate
  cc-tools mcp list
  cc-tools mcp enable jira
`)
}

func runStatusline() {
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

func loadValidateConfig() (int, int) {
	timeoutSecs := 60
	cooldownSecs := 5

	// Load configuration
	cfg, _ := config.Load()
	if cfg != nil {
		if cfg.Hooks.Validate.TimeoutSeconds > 0 {
			timeoutSecs = cfg.Hooks.Validate.TimeoutSeconds
		}
		if cfg.Hooks.Validate.CooldownSeconds > 0 {
			cooldownSecs = cfg.Hooks.Validate.CooldownSeconds
		}
	}

	// Environment variables override config
	if envTimeout := os.Getenv("CC_TOOLS_HOOKS_VALIDATE_TIMEOUT_SECONDS"); envTimeout != "" {
		if val, err := strconv.Atoi(envTimeout); err == nil && val > 0 {
			timeoutSecs = val
		}
	}
	if envCooldown := os.Getenv("CC_TOOLS_HOOKS_VALIDATE_COOLDOWN_SECONDS"); envCooldown != "" {
		if val, err := strconv.Atoi(envCooldown); err == nil && val >= 0 {
			cooldownSecs = val
		}
	}

	return timeoutSecs, cooldownSecs
}

func runValidate() {
	timeoutSecs, cooldownSecs := loadValidateConfig()
	debug := os.Getenv("CLAUDE_HOOKS_DEBUG") == "1"

	exitCode := hooks.ValidateWithSkipCheck(
		context.Background(),
		os.Stdin,
		os.Stdout,
		os.Stderr,
		debug,
		timeoutSecs,
		cooldownSecs,
	)
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
	// Create or append to debug log file for current directory
	debugFile := getDebugLogPath()
	//nolint:gosec // Debug log file path is controlled
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

// getDebugLogPath returns the debug log path for the current directory.
func getDebugLogPath() string {
	wd, err := os.Getwd()
	if err != nil {
		// Fallback to generic log if we can't get working directory
		return "/tmp/cc-tools.debug"
	}
	return shared.GetDebugLogPathForDir(wd)
}
