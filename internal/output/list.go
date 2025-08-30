package output

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// ListRenderer provides beautiful list formatting.
type ListRenderer struct {
	titleStyle  lipgloss.Style
	itemStyle   lipgloss.Style
	bulletStyle lipgloss.Style
	bullet      string
	indent      string
}

// NewListRenderer creates a new list renderer with default styling.
func NewListRenderer() *ListRenderer {
	return &ListRenderer{
		titleStyle:  lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#cba6f7")), // Mauve
		itemStyle:   lipgloss.NewStyle().Foreground(lipgloss.Color("#cdd6f4")),            // Text
		bulletStyle: lipgloss.NewStyle().Foreground(lipgloss.Color("#89dceb")),            // Sky
		bullet:      "•",
		indent:      "  ",
	}
}

// Render formats a title and list of items.
func (l *ListRenderer) Render(title string, items []string) string {
	var sb strings.Builder

	if title != "" {
		sb.WriteString(l.titleStyle.Render(title))
		sb.WriteString("\n")
	}

	for _, item := range items {
		sb.WriteString(l.indent)
		sb.WriteString(l.bulletStyle.Render(l.bullet))
		sb.WriteString(" ")
		sb.WriteString(l.itemStyle.Render(item))
		sb.WriteString("\n")
	}

	return sb.String()
}

// RenderMap formats a title and map of key-value pairs.
func (l *ListRenderer) RenderMap(title string, items map[string]string) string {
	var sb strings.Builder

	if title != "" {
		sb.WriteString(l.titleStyle.Render(title))
		sb.WriteString("\n")
	}

	// Find the longest key for alignment
	maxKeyLen := 0
	for key := range items {
		if len(key) > maxKeyLen {
			maxKeyLen = len(key)
		}
	}

	for key, value := range items {
		sb.WriteString(l.indent)

		// Style the key with padding
		styledKey := l.bulletStyle.Render(fmt.Sprintf("%-*s", maxKeyLen, key))
		sb.WriteString(styledKey)
		sb.WriteString(": ")
		sb.WriteString(l.itemStyle.Render(value))
		sb.WriteString("\n")
	}

	return sb.String()
}

// RenderGrouped formats items grouped by category.
func (l *ListRenderer) RenderGrouped(title string, groups map[string][]string) string {
	var sb strings.Builder

	if title != "" {
		sb.WriteString(l.titleStyle.Render(title))
		sb.WriteString("\n")
	}

	for group, items := range groups {
		sb.WriteString(l.indent)
		sb.WriteString(l.bulletStyle.Render(group))
		sb.WriteString(":\n")

		for _, item := range items {
			sb.WriteString(l.indent)
			sb.WriteString(l.indent)
			sb.WriteString(l.itemStyle.Render("- " + item))
			sb.WriteString("\n")
		}
	}

	return sb.String()
}
