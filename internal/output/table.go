package output

import (
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/lipgloss/table"
)

// TableInterface defines the interface for table rendering.
type TableInterface interface {
	AddRow([]string)
	Render() string
}

// TableRenderer provides simple table rendering without interactivity.
type TableRenderer struct {
	headers []string
	widths  []int
	rows    [][]string
}

// NewTable creates a new table renderer with the given columns.
func NewTable(headers []string, widths []int) *TableRenderer {
	// Ensure columns and widths match - caller should ensure this
	// but we'll handle gracefully by using minimum length
	minLen := len(headers)
	if len(widths) < minLen {
		minLen = len(widths)
	}

	// Trim to minimum length
	if len(headers) > minLen {
		headers = headers[:minLen]
	}
	if len(widths) > minLen {
		widths = widths[:minLen]
	}

	return &TableRenderer{
		headers: headers,
		widths:  widths,
		rows:    [][]string{},
	}
}

// AddRow adds a row to the table.
func (tr *TableRenderer) AddRow(row []string) {
	tr.rows = append(tr.rows, row)
}

// Render returns the rendered table as a string.
func (tr *TableRenderer) Render() string {
	// Create styles
	headerStyle := lipgloss.NewStyle().
		Foreground(lipgloss.Color("252")).
		Bold(true).
		Padding(0, 1)

	cellStyle := lipgloss.NewStyle().
		Padding(0, 1)

	// Calculate total width
	totalWidth := 0
	for _, w := range tr.widths {
		totalWidth += w
	}

	// Create the lipgloss table
	t := table.New(). //nolint:forbidigo // External library function
				Border(lipgloss.NormalBorder()).
				BorderStyle(lipgloss.NewStyle().Foreground(lipgloss.Color("240"))).
				Headers(tr.headers...).
				Rows(tr.rows...).
				Width(totalWidth).
				StyleFunc(func(row, _ int) lipgloss.Style {
			if row == 0 {
				return headerStyle
			}
			return cellStyle
		})

	return t.String()
}
