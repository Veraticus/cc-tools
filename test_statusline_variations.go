package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	
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
	fmt.Printf("Terminal width: %d\n", termWidth)
	fmt.Println(strings.Repeat("=", termWidth))

	// Set up environment
	os.Setenv("COLUMNS", strconv.Itoa(termWidth))

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
		fmt.Printf("\n%d. %s\n", i+1, tc.Name)

		// Build JSON input
		input := buildJSONInput(tc)

		// Set environment variables
		if tc.HasDevspace {
			os.Setenv("TMUX_DEVSPACE", "mars")
		} else {
			os.Unsetenv("TMUX_DEVSPACE")
		}

		if tc.HasHostname {
			os.Setenv("HOSTNAME", "vermissian")
		} else {
			os.Unsetenv("HOSTNAME")
		}

		if tc.HasAWS {
			os.Setenv("AWS_PROFILE", "dev-account")
		} else {
			os.Unsetenv("AWS_PROFILE")
		}

		// Clear cache
		exec.Command("rm", "-f", "/dev/shm/claude_statusline_*").Run()

		// Set terminal width for statusline commands
		os.Setenv("CLAUDE_STATUSLINE_WIDTH", strconv.Itoa(termWidth))

		// Run Go statusline
		runStatusline("./build/statusline", input, termWidth)

		fmt.Println()
	}
}

func buildJSONInput(tc TestCase) string {
	data := map[string]interface{}{
		"model": map[string]interface{}{
			"display_name": "Claude 3 Opus",
		},
		"workspace": map[string]interface{}{
			"project_dir": os.Getenv("HOME") + "/Personal/cc-tools",
		},
	}

	if tc.HasGit {
		data["git_info"] = map[string]interface{}{
			"branch":       "main",
			"is_git_repo":  true,
			"has_modified": true,
		}
	}

	// Add transcript path for context bar
	if tc.HasContext {
		// Create a mock transcript with appropriate token count
		transcriptPath := "/tmp/mock_transcript.jsonl"
		var contextTokens int

		// Determine token count based on test name
		if strings.Contains(tc.Name, "25%") {
			contextTokens = 40000 // 25% of 160k
		} else if strings.Contains(tc.Name, "50%") {
			contextTokens = 80000 // 50% of 160k
		} else if strings.Contains(tc.Name, "75%") {
			contextTokens = 120000 // 75% of 160k
		} else if strings.Contains(tc.Name, "100%") {
			contextTokens = 160000 // 100% of 160k
		} else if tc.IsCompact {
			contextTokens = 130000 // Just over compact threshold
		} else {
			contextTokens = 80000 // Default 50%
		}

		// Create mock transcript file
		transcript := fmt.Sprintf(`{"message": {"usage": {"input_tokens": %d, "output_tokens": 5000}}}`, contextTokens)
		os.WriteFile(transcriptPath, []byte(transcript), 0644)
		data["transcript_path"] = transcriptPath
	}

	// For compact mode without context, just set high cost to show we're in a long session
	if tc.IsCompact && !tc.HasContext {
		data["cost"] = map[string]interface{}{
			"total_cost_usd": 5.50,
			"input_tokens":   500000,
			"output_tokens":  100000,
		}
	}

	// Mock K8s context by creating a fake kubeconfig
	if tc.HasK8s {
		kubeconfig := "/tmp/mock_kubeconfig"
		os.WriteFile(kubeconfig, []byte("current-context: production-cluster\n"), 0644)
		os.Setenv("CLAUDE_STATUSLINE_KUBECONFIG", kubeconfig)
	} else {
		os.Setenv("CLAUDE_STATUSLINE_KUBECONFIG", "/dev/null")
	}

	jsonBytes, _ := json.Marshal(data)
	return string(jsonBytes)
}

func runStatusline(command string, input string, termWidth int) {
	cmd := exec.Command("bash", "-c", fmt.Sprintf("echo '%s' | CLAUDE_STATUSLINE_WIDTH=%d %s 2>/dev/null", input, termWidth, command))
	output, _ := cmd.Output()
	if len(output) > 0 {
		fmt.Print(string(output))
		// Measure the rune width
		clean := stripAnsiCodes(string(output))
		width := runewidth.StringWidth(clean)
		fmt.Printf("\n  Width: %d, Terminal: %d, Diff: %+d", width, termWidth, width-termWidth)
		
		// Check for trailing spaces
		if len(clean) > 0 && clean[len(clean)-1] == ' ' {
			// Count trailing spaces
			spaces := 0
			for i := len(clean) - 1; i >= 0 && clean[i] == ' '; i-- {
				spaces++
			}
			fmt.Printf(" [%d trailing space(s)]", spaces)
		}
	} else {
		fmt.Print("[no output]")
	}
}

func stripAnsiCodes(text string) string {
	var result strings.Builder
	inEscape := false
	for _, r := range text {
		if r == '\033' {
			inEscape = true
		} else if inEscape {
			if r == 'm' {
				inEscape = false
			}
		} else {
			result.WriteRune(r)
		}
	}
	return result.String()
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
		cmd := exec.Command("tmux", "display-message", "-p", "#{window_width}")
		if output, err := cmd.Output(); err == nil {
			if width, err := strconv.Atoi(strings.TrimSpace(string(output))); err == nil && width > 0 {
				return width
			}
		}
	}

	// Try tput
	cmd := exec.Command("tput", "cols")
	output, err := cmd.Output()
	if err == nil {
		if width, err := strconv.Atoi(strings.TrimSpace(string(output))); err == nil && width > 0 {
			return width
		}
	}

	// Default fallback
	return 200
}

