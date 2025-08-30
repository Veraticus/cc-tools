package output

import (
	"strings"
)

// MockTableRenderer provides a mock table renderer for testing.
type MockTableRenderer struct {
	headers []string
	widths  []int
	rows    [][]string
}

// NewMockTable creates a new mock table renderer.
func NewMockTable(headers []string, widths []int) *MockTableRenderer {
	return &MockTableRenderer{
		headers: headers,
		widths:  widths,
		rows:    [][]string{},
	}
}

// AddRow adds a row to the mock table.
func (m *MockTableRenderer) AddRow(row []string) {
	m.rows = append(m.rows, row)
}

// Render returns a simple text representation for testing.
func (m *MockTableRenderer) Render() string {
	var result strings.Builder

	// Header
	result.WriteString(strings.Join(m.headers, " | "))
	result.WriteString("\n")

	// Rows
	for _, row := range m.rows {
		result.WriteString(strings.Join(row, " | "))
		result.WriteString("\n")
	}

	return result.String()
}

// GetRows returns the rows for testing assertions.
func (m *MockTableRenderer) GetRows() [][]string {
	return m.rows
}

// GetHeaders returns the headers for testing assertions.
func (m *MockTableRenderer) GetHeaders() []string {
	return m.headers
}
