// Package main implements the cc-tools CLI application.
package main

import (
	"bytes"
	"context"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"strings"
	"time"

	"github.com/Veraticus/cc-tools/internal/config"
	"github.com/Veraticus/cc-tools/internal/hooks"
	"github.com/Veraticus/cc-tools/internal/server"
	"github.com/Veraticus/cc-tools/internal/statusline"
)

const minArgs = 2

// Build-time variables
var (
	version   = "dev"
	buildTime = "unknown"
)

func main() {
	// Debug logging - log all invocations to a file
	debugLog()

	if len(os.Args) < minArgs {
		printUsage()
		os.Exit(1)
	}

	switch os.Args[1] {
	case "statusline":
		runStatuslineWithServer()
	case "lint":
		runLintWithServer()
	case "test":
		runTestWithServer()
	case "serve":
		runServe()
	case "status":
		runStatus()
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
  serve         Run server mode for improved performance
  status        Check server status
  version       Print version information
  help          Show this help message

Examples:
  echo '{"cwd": "/path"}' | cc-tools statusline
  echo '{"file_path": "main.go"}' | cc-tools lint
  echo '{"file_path": "main_test.go"}' | cc-tools test
`)
}

func runStatus() {
	socketPath := os.Getenv("CC_TOOLS_SOCKET")
	if socketPath == "" {
		socketPath = server.DefaultSocketPath()
	}

	// Check if socket exists
	if _, err := os.Stat(socketPath); os.IsNotExist(err) {
		// Print status to stdout as intended output
		fmt.Printf("Server: NOT RUNNING\nSocket: %s (not found)\n", socketPath) //nolint:forbidigo // CLI output
		os.Exit(1)
	}

	// Try to connect and get stats
	client := server.NewClient(socketPath)
	stats, _, _, err := client.Call("stats", "")
	if err != nil {
		// Print status to stdout as intended output
		fmt.Printf("Server: ERROR\nSocket: %s\nError: %v\n", socketPath, err) //nolint:forbidigo // CLI output
		os.Exit(1)
	}

	// Print server stats to stdout as intended output
	fmt.Print(stats) //nolint:forbidigo // CLI output
}

func runServe() {
	// Parse flags
	fs := flag.NewFlagSet("serve", flag.ExitOnError)
	socketPath := fs.String("socket", server.DefaultSocketPath(), "Socket path")
	verbose := fs.Bool("verbose", false, "Verbose logging")
	if err := fs.Parse(os.Args[2:]); err != nil {
		log.Fatalf("Parse flags: %v", err)
	}

	// Create logger
	logger := server.NewStandardLogger()
	if !*verbose {
		log.SetOutput(io.Discard)
	}

	// Load configuration
	cfg, err := config.Load()
	if err != nil {
		logger.Printf("Warning: Failed to load config: %v", err)
		// Continue with defaults
		cfg = &config.Config{}
	} else {
		if configFile := config.ConfigFileUsed(); configFile != "" {
			logger.Printf("Loaded config from: %s", configFile)
		}

		// Log notification configuration if present
		if cfg.Notifications.NtfyTopic != "" {
			logger.Printf("Notifications configured for ntfy topic: %s", cfg.Notifications.NtfyTopic)
		}
	}

	// Create dependencies using config values with fallback defaults
	lintTimeout := 30
	lintCooldown := 2
	testTimeout := 60
	testCooldown := 2
	
	if cfg != nil {
		if cfg.Hooks.Lint.TimeoutSeconds > 0 {
			lintTimeout = cfg.Hooks.Lint.TimeoutSeconds
		}
		if cfg.Hooks.Lint.CooldownSeconds > 0 {
			lintCooldown = cfg.Hooks.Lint.CooldownSeconds
		}
		if cfg.Hooks.Test.TimeoutSeconds > 0 {
			testTimeout = cfg.Hooks.Test.TimeoutSeconds
		}
		if cfg.Hooks.Test.CooldownSeconds > 0 {
			testCooldown = cfg.Hooks.Test.CooldownSeconds
		}
	}
	
	deps := &server.ServerDependencies{
		LintRunner:  server.NewHookLintRunner(true, lintTimeout, lintCooldown),
		TestRunner:  server.NewHookTestRunner(true, testTimeout, testCooldown),
		LockManager: server.NewSimpleLockManager(),
		Logger:      logger,
	}

	// Create and run server
	srv := server.NewServer(*socketPath, deps)

	logger.Printf("Starting server on %s", *socketPath)
	if err := srv.Run(); err != nil {
		log.Fatalf("Server error: %v", err)
	}
}

func runStatuslineWithServer() {
	// Statusline always runs locally to have access to user environment
	// (HOSTNAME, AWS_PROFILE, TMUX_DEVSPACE, kubeconfig, etc.)
	result, err := runStatuslineDirect()
	if err != nil {
		// Fallback prompt output to stdout
		fmt.Print(" > ") //nolint:forbidigo // CLI output
		os.Exit(0)
	}
	// Output statusline result to stdout
	fmt.Print(result) //nolint:forbidigo // CLI output
}

func isResourceLockedError(err error) bool {
	if err == nil {
		return false
	}
	// Check if the error message contains "Resource locked"
	return strings.Contains(err.Error(), "Resource locked")
}

func runLintWithServer() {
	result, exitCode, err := server.TryCallWithFallback("lint", runLintDirect)

	// Debug log the result
	if debugFile, err := os.OpenFile("/tmp/cc-tools.debug", os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644); err == nil {
		defer debugFile.Close()
		fmt.Fprintf(debugFile, "Lint result: %s\n", result)
		fmt.Fprintf(debugFile, "Lint exit code: %d\n", exitCode)
		if err != nil {
			fmt.Fprintf(debugFile, "Lint error: %v\n", err)
		}
	}

	if err != nil {
		// Return exit code 0 if resource is locked to match direct execution behavior
		if isResourceLockedError(err) {
			os.Exit(0)
		}
		os.Exit(1)
	}

	// Print result to stdout if any
	if result != "" {
		fmt.Print(result)
	}

	// Exit with the exit code from the server/hook
	os.Exit(exitCode)
}

func runTestWithServer() {
	result, exitCode, err := server.TryCallWithFallback("test", runTestDirect)
	if err != nil {
		// Return exit code 0 if resource is locked to match direct execution behavior
		if isResourceLockedError(err) {
			os.Exit(0)
		}
		os.Exit(1)
	}

	// Print result to stdout if any
	if result != "" {
		fmt.Print(result)
	}

	// Exit with the exit code from the server/hook
	os.Exit(exitCode)
}

func runStatuslineDirect() (string, error) {
	// Read stdin
	input, err := io.ReadAll(os.Stdin)
	if err != nil {
		return "", fmt.Errorf("reading stdin: %w", err)
	}

	// Recreate stdin reader
	reader := bytes.NewReader(input)

	return runStatuslineWithInput(reader)
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

func runLintDirect() (string, error) {
	const (
		debug        = true
		timeoutSecs  = 30
		cooldownSecs = 2
	)

	exitCode := hooks.RunSmartHook(context.Background(), hooks.CommandTypeLint, debug, timeoutSecs, cooldownSecs, nil)
	if exitCode != 0 {
		return "", fmt.Errorf("lint failed with exit code %d", exitCode)
	}
	return "", nil
}

func runTestDirect() (string, error) {
	const (
		debug        = true
		timeoutSecs  = 60
		cooldownSecs = 2
	)

	exitCode := hooks.RunSmartHook(context.Background(), hooks.CommandTypeTest, debug, timeoutSecs, cooldownSecs, nil)
	if exitCode != 0 {
		return "", fmt.Errorf("test failed with exit code %d", exitCode)
	}
	return "", nil
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

// Global variable to store stdin for debugging
var stdinDebugData []byte

func debugLog() {
	// Create or append to debug log file
	debugFile := "/tmp/cc-tools.debug"
	f, err := os.OpenFile(debugFile, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return // Silently fail if we can't write debug log
	}
	defer f.Close()

	// Read stdin and save it for both debug and actual use
	if stat, _ := os.Stdin.Stat(); (stat.Mode() & os.ModeCharDevice) == 0 {
		// There's data in stdin
		stdinDebugData, _ = io.ReadAll(os.Stdin)
		// Create a new reader from the data we just read
		// This will be used by the actual commands
		os.Stdin, _ = os.Open("/dev/stdin") // Reset stdin
		// Actually, we need to pipe it back - create a temp file
		if tmpFile, err := os.CreateTemp("", "cc-tools-stdin-"); err == nil {
			tmpFile.Write(stdinDebugData)
			tmpFile.Seek(0, 0)
			os.Stdin = tmpFile
		}
	}

	// Log the invocation details
	timestamp := time.Now().Format("2006-01-02 15:04:05.000")
	fmt.Fprintf(f, "\n========================================\n")
	fmt.Fprintf(f, "[%s] cc-tools invoked\n", timestamp)
	fmt.Fprintf(f, "Args: %v\n", os.Args)
	fmt.Fprintf(f, "Environment:\n")
	fmt.Fprintf(f, "  CC_TOOLS_SOCKET: %s\n", os.Getenv("CC_TOOLS_SOCKET"))
	fmt.Fprintf(f, "  CC_TOOLS_NO_SERVER: %s\n", os.Getenv("CC_TOOLS_NO_SERVER"))
	fmt.Fprintf(f, "  CLAUDE_HOOKS_DEBUG: %s\n", os.Getenv("CLAUDE_HOOKS_DEBUG"))
	fmt.Fprintf(f, "  Working Dir: %s\n", func() string {
		if wd, err := os.Getwd(); err == nil {
			return wd
		}
		return "unknown"
	}())

	if len(stdinDebugData) > 0 {
		fmt.Fprintf(f, "Stdin: %s\n", string(stdinDebugData))
	} else {
		fmt.Fprintf(f, "Stdin: (no data available)\n")
	}

	fmt.Fprintf(f, "Command: %s\n", func() string {
		if len(os.Args) > 1 {
			return os.Args[1]
		}
		return "(none)"
	}())
}
