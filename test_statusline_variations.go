// Package main provides test utilities for testing statusline variations.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/mattn/go-runewidth"
)

type TestCase struct {
	Name        string
	HasContext  bool
	HasDevspace bool
	HasHostname bool
	HasGit      bool
	HasAWS      bool
	HasK8s      bool
	IsCompact   bool // High token count to trigger compact mode
}

func main() {
	// Get actual terminal width
	termWidth := getTerminalWidth()
	fmt.Printf("Terminal width: %d\n", termWidth) //nolint:forbidigo // Test output
	fmt.Println(strings.Repeat("=", termWidth))   //nolint:forbidigo // Test output

	// Set up environment
	if err := os.Setenv("COLUMNS", strconv.Itoa(termWidth)); err != nil {
		fmt.Fprintf(os.Stderr, "Failed to set COLUMNS: %v\n", err)
	}

	testCases := []TestCase{
		// Minimal
		{"Minimal (dir + model only)", false, false, false, false, false, false, false},

		// Individual components
		{"With Git", false, false, false, true, false, false, false},
		{"With Hostname", false, false, true, false, false, false, false},
		{"With AWS", false, false, false, false, true, false, false},
		{"With K8s", false, false, false, false, false, true, false},
		{"With Devspace", false, true, false, false, false, false, false},
		{"With Context Bar (50%)", true, false, false, false, false, false, false},

		// Common combinations
		{"Git + Hostname", false, false, true, true, false, false, false},
		{"Git + AWS + K8s", false, false, false, true, true, true, false},
		{"All components", true, true, true, true, true, true, false},

		// Compact mode versions
		{"All components (compact)", true, true, true, true, true, true, true},
		{"Minimal (compact)", false, false, false, false, false, false, true},
		{"Git + AWS (compact)", false, false, false, true, true, false, true},

		// Context bar at different levels
		{"Context 25% (green)", true, false, true, true, false, false, false},
		{"Context 50% (yellow)", true, false, true, true, false, false, false},
		{"Context 75% (peach)", true, false, true, true, false, false, false},
		{"Context 100% (red)", true, false, true, true, false, false, false},
	}

	for i, tc := range testCases {
		fmt.Printf("\n%d. %s\n", i+1, tc.Name) //nolint:forbidigo // Test output
		runTestCase(tc, termWidth)
		fmt.Println() //nolint:forbidigo // Test output
	}
}

func runTestCase(tc TestCase, termWidth int) {
	// Build JSON input
	input := buildJSONInput(tc)

	// Set environment variables
	setupTestEnvironment(tc, termWidth)

	// Clear cache
	clearCache()

	// Run Go statusline
	runStatusline("./build/statusline", input, termWidth)
}

func setupTestEnvironment(tc TestCase, termWidth int) {
	setEnvVar("TMUX_DEVSPACE", "mars", tc.HasDevspace)
	setEnvVar("HOSTNAME", "vermissian", tc.HasHostname)
	setEnvVar("AWS_PROFILE", "dev-account", tc.HasAWS)

	// Set terminal width for statusline commands
	if err := os.Setenv("CLAUDE_STATUSLINE_WIDTH", strconv.Itoa(termWidth)); err != nil {
		fmt.Fprintf(os.Stderr, "Failed to set CLAUDE_STATUSLINE_WIDTH: %v\n", err)
	}
}

func setEnvVar(key, value string, shouldSet bool) {
	if shouldSet {
		if err := os.Setenv(key, value); err != nil {
			fmt.Fprintf(os.Stderr, "Failed to set %s: %v\n", key, err)
		}
	} else {
		if err := os.Unsetenv(key); err != nil {
			fmt.Fprintf(os.Stderr, "Failed to unset %s: %v\n", key, err)
		}
	}
}

func clearCache() {
	const cacheTimeout = 5 * time.Second
	ctx, cancel := context.WithTimeout(context.Background(), cacheTimeout)
	defer cancel()
	if err := exec.CommandContext(ctx, "rm", "-f", "/dev/shm/claude_statusline_*").Run(); err != nil {
		// Cache clear failed - continue anyway
		_ = err
	}
}

func buildJSONInput(tc TestCase) string {
	data := map[string]any{
		"model": map[string]any{
			"display_name": "Claude 3 Opus",
		},
		"workspace": map[string]any{
			"project_dir": os.Getenv("HOME") + "/Personal/cc-tools",
		},
	}

	if tc.HasGit {
		data["git_info"] = map[string]any{
			"branch":       "main",
			"is_git_repo":  true,
			"has_modified": true,
		}
	}

	// Add transcript path for context bar
	if tc.HasContext {
		addContextData(data, tc)
	}

	// For compact mode without context, just set high cost to show we're in a long session
	if tc.IsCompact && !tc.HasContext {
		const (
			sampleCostUSD      = 5.50
			sampleInputTokens  = 500000
			sampleOutputTokens = 100000
		)
		data["cost"] = map[string]any{
			"total_cost_usd": sampleCostUSD,
			"input_tokens":   sampleInputTokens,
			"output_tokens":  sampleOutputTokens,
		}
	}

	// Mock K8s context by creating a fake kubeconfig
	setupK8sConfig(tc.HasK8s)

	jsonBytes, _ := json.Marshal(data)
	return string(jsonBytes)
}

func runStatusline(command string, input string, termWidth int) {
	const commandTimeout = 10 * time.Second
	ctx, cancel := context.WithTimeout(context.Background(), commandTimeout)
	defer cancel()
	cmd := exec.CommandContext( //nolint:gosec // Test command with controlled inputs
		ctx,
		"bash",
		"-c",
		fmt.Sprintf("echo '%s' | CLAUDE_STATUSLINE_WIDTH=%d %s 2>/dev/null", input, termWidth, command),
	)
	output, _ := cmd.Output()
	if len(output) > 0 {
		fmt.Print(string(output)) //nolint:forbidigo // Test output
		// Measure the rune width
		clean := stripAnsiCodes(string(output))
		width := runewidth.StringWidth(clean)
		// Display width information (test output)
		fmt.Printf( //nolint:forbidigo // Test output
			"\n  Width: %d, Terminal: %d, Diff: %+d",
			width, termWidth, width-termWidth,
		)

		// Check for trailing spaces
		if clean != "" && clean[len(clean)-1] == ' ' {
			// Count trailing spaces
			spaces := 0
			for i := len(clean) - 1; i >= 0 && clean[i] == ' '; i-- {
				spaces++
			}
			fmt.Printf(" [%d trailing space(s)]", spaces) //nolint:forbidigo // Test output
		}
	} else {
		fmt.Print("[no output]") //nolint:forbidigo // Test output
	}
}

func stripAnsiCodes(text string) string {
	var result strings.Builder
	inEscape := false
	for _, r := range text {
		switch {
		case r == '\033':
			inEscape = true
		case inEscape:
			if r == 'm' {
				inEscape = false
			}
		default:
			result.WriteRune(r)
		}
	}
	return result.String()
}

func addContextData(data map[string]any, tc TestCase) {
	transcriptPath := "/tmp/mock_transcript.jsonl"
	contextTokens := getContextTokens(tc)

	// Create mock transcript file
	transcript := fmt.Sprintf(`{"message": {"usage": {"input_tokens": %d, "output_tokens": 5000}}}`, contextTokens)
	const fileMode = 0600
	if err := os.WriteFile(transcriptPath, []byte(transcript), fileMode); err != nil {
		fmt.Fprintf(os.Stderr, "Failed to write transcript: %v\n", err)
	}
	data["transcript_path"] = transcriptPath
}

func getContextTokens(tc TestCase) int {
	const (
		quarterTokens      = 40000  // 25% of 160k
		halfTokens         = 80000  // 50% of 160k
		threeQuarterTokens = 120000 // 75% of 160k
		fullTokens         = 160000 // 100% of 160k
		compactTokens      = 130000 // Just over compact threshold
	)

	switch {
	case strings.Contains(tc.Name, "25%"):
		return quarterTokens
	case strings.Contains(tc.Name, "50%"):
		return halfTokens
	case strings.Contains(tc.Name, "75%"):
		return threeQuarterTokens
	case strings.Contains(tc.Name, "100%"):
		return fullTokens
	case tc.IsCompact:
		return compactTokens
	default:
		return halfTokens // Default 50%
	}
}

func setupK8sConfig(hasK8s bool) {
	if hasK8s {
		kubeconfig := "/tmp/mock_kubeconfig"
		if err := os.WriteFile(kubeconfig, []byte("current-context: production-cluster\n"), 0600); err != nil {
			fmt.Fprintf(os.Stderr, "Failed to write kubeconfig: %v\n", err)
		}
		if err := os.Setenv("CLAUDE_STATUSLINE_KUBECONFIG", kubeconfig); err != nil {
			fmt.Fprintf(os.Stderr, "Failed to set CLAUDE_STATUSLINE_KUBECONFIG: %v\n", err)
		}
	} else {
		if err := os.Setenv("CLAUDE_STATUSLINE_KUBECONFIG", "/dev/null"); err != nil {
			fmt.Fprintf(os.Stderr, "Failed to set CLAUDE_STATUSLINE_KUBECONFIG: %v\n", err)
		}
	}
}

func getTerminalWidth() int {
	// Check COLUMNS env var first
	if columns := os.Getenv("COLUMNS"); columns != "" {
		if width, err := strconv.Atoi(columns); err == nil && width > 0 {
			return width
		}
	}

	// Check if we're in tmux and get width directly from tmux
	if tmux := os.Getenv("TMUX"); tmux != "" {
		const commandTimeout = 5 * time.Second
		ctx, cancel := context.WithTimeout(context.Background(), commandTimeout)
		defer cancel()
		cmd := exec.CommandContext(ctx, "tmux", "display-message", "-p", "#{window_width}")
		if output, err := cmd.Output(); err == nil {
			if width, parseErr := strconv.Atoi(strings.TrimSpace(string(output))); parseErr == nil && width > 0 {
				return width
			}
		}
	}

	// Try tput
	const commandTimeout = 5 * time.Second
	ctx, cancel := context.WithTimeout(context.Background(), commandTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "tput", "cols")
	output, err := cmd.Output()
	if err == nil {
		if width, parseErr := strconv.Atoi(strings.TrimSpace(string(output))); parseErr == nil && width > 0 {
			return width
		}
	}

	// Default fallback
	const defaultTermWidth = 200
	return defaultTermWidth
}
