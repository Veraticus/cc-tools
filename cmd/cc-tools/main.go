package main

import (
	"fmt"
	"os"
	"time"

	"github.com/Veraticus/cc-tools/internal/hooks"
	"github.com/Veraticus/cc-tools/internal/statusline"
)

func main() {
	if len(os.Args) < 2 {
		printUsage()
		os.Exit(1)
	}

	switch os.Args[1] {
	case "statusline":
		runStatusline()
	case "lint", "smart-lint":
		runLint()
	case "test", "smart-test":
		runTest()
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
  lint          Run smart linting (alias: smart-lint)
  test          Run smart testing (alias: smart-test)
  version       Print version information
  help          Show this help message

Examples:
  echo '{"cwd": "/path"}' | cc-tools statusline
  echo '{"file_path": "main.go"}' | cc-tools lint
  echo '{"file_path": "main_test.go"}' | cc-tools test
`)
}

func runStatusline() {
	deps := &statusline.Dependencies{
		FileReader:    &statusline.DefaultFileReader{},
		CommandRunner: &statusline.DefaultCommandRunner{},
		EnvReader:     &statusline.DefaultEnvReader{},
		TerminalWidth: &statusline.DefaultTerminalWidth{},
		CacheDir:      getCacheDir(),
		CacheDuration: getCacheDuration(),
	}

	sl := statusline.New(deps)

	result, err := sl.Generate(os.Stdin)
	if err != nil {
		fmt.Print(" > ")
		os.Exit(0)
	}

	fmt.Print(result)
}

func runLint() {
	const (
		debug        = true
		timeoutSecs  = 30
		cooldownSecs = 2
	)

	exitCode := hooks.RunSmartHook(hooks.CommandTypeLint, debug, timeoutSecs, cooldownSecs)
	os.Exit(exitCode)
}

func runTest() {
	const (
		debug        = true
		timeoutSecs  = 60
		cooldownSecs = 2
	)

	exitCode := hooks.RunSmartHook(hooks.CommandTypeTest, debug, timeoutSecs, cooldownSecs)
	os.Exit(exitCode)
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