// Package main implements the statusline CLI tool for Claude Code.
package main

import (
	"fmt"
	"os"

	"github.com/Veraticus/cc-tools/internal/hooks"
	"github.com/Veraticus/cc-tools/internal/statusline"
)

func main() {
	// Read statusline input from stdin
	input, err := hooks.ReadStatusLineInput()
	if err != nil {
		// If no input, output a simple default statusline
		fmt.Println("[Claude Code]") //nolint:forbidigo // Statusline must output to stdout
		os.Exit(0)
	}

	// Create formatter and generate statusline
	formatter := statusline.NewFormatter(input)
	output := formatter.Format()

	// Output the statusline (first line only as per spec)
	fmt.Println(output) //nolint:forbidigo // Statusline must output to stdout
}
