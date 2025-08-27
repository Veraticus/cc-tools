package main

import (
	"bytes"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"time"

	"github.com/Veraticus/cc-tools/internal/hooks"
	"github.com/Veraticus/cc-tools/internal/server"
	"github.com/Veraticus/cc-tools/internal/statusline"
)

func main() {
	if len(os.Args) < 2 {
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
		fmt.Println("cc-tools v0.1.0")
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
		fmt.Printf("Server: NOT RUNNING\nSocket: %s (not found)\n", socketPath)
		os.Exit(1)
	}

	// Try to connect and get stats
	client := server.NewClient(socketPath)
	stats, _, err := client.Call("stats", "")
	if err != nil {
		fmt.Printf("Server: ERROR\nSocket: %s\nError: %v\n", socketPath, err)
		os.Exit(1)
	}

	fmt.Print(stats)
}

func runServe() {
	// Parse flags
	fs := flag.NewFlagSet("serve", flag.ExitOnError)
	socketPath := fs.String("socket", server.DefaultSocketPath(), "Socket path")
	verbose := fs.Bool("verbose", false, "Verbose logging")
	fs.Parse(os.Args[2:])

	// Create logger
	logger := &server.StandardLogger{}
	if !*verbose {
		log.SetOutput(io.Discard)
	}

	// Create dependencies
	deps := &server.ServerDependencies{
		LintRunner:  server.NewHookLintRunner(true, 30, 2),
		TestRunner:  server.NewHookTestRunner(true, 60, 2),
		Statusline:  server.NewStatuslineRunner("/dev/shm", 20),
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
	result, err := server.TryCallWithFallback("statusline", runStatuslineDirect)
	if err != nil {
		fmt.Print(" > ") // Fallback prompt
		os.Exit(0)
	}
	fmt.Print(result)
}

func runLintWithServer() {
	_, err := server.TryCallWithFallback("lint", runLintDirect)
	if err != nil {
		os.Exit(1)
	}
}

func runTestWithServer() {
	_, err := server.TryCallWithFallback("test", runTestDirect)
	if err != nil {
		os.Exit(1)
	}
}

func runStatuslineDirect() (string, error) {
	// Read stdin
	input, err := io.ReadAll(os.Stdin)
	if err != nil {
		return "", err
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

	sl := statusline.New(deps)

	result, err := sl.Generate(reader)
	if err != nil {
		return "", err
	}

	return result, nil
}

func runLintDirect() (string, error) {
	const (
		debug        = true
		timeoutSecs  = 30
		cooldownSecs = 2
	)

	exitCode := hooks.RunSmartHook(hooks.CommandTypeLint, debug, timeoutSecs, cooldownSecs)
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

	exitCode := hooks.RunSmartHook(hooks.CommandTypeTest, debug, timeoutSecs, cooldownSecs)
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
	return 20 * time.Second
}
