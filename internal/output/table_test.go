package output

import (
	"strings"
	"testing"
)

func TestTableInterface(_ *testing.T) {
	// Verify that both TableRenderer and MockTableRenderer implement TableInterface
	var _ TableInterface = (*TableRenderer)(nil)
	var _ TableInterface = (*MockTableRenderer)(nil)
}

func TestNewTable(t *testing.T) {
	tests := []struct {
		name    string
		headers []string
		widths  []int
		rows    [][]string
		check   func(t *testing.T, result string)
	}{
		{
			name:    "empty table",
			headers: []string{"Column1", "Column2"},
			widths:  []int{10, 10},
			rows:    [][]string{},
			check: func(t *testing.T, result string) {
				t.Helper()
				if !strings.Contains(result, "Column1") {
					t.Errorf("Expected Column1 in header, got: %s", result)
				}
				if !strings.Contains(result, "Column2") {
					t.Errorf("Expected Column2 in header, got: %s", result)
				}
			},
		},
		{
			name:    "table with data",
			headers: []string{"Name", "Value"},
			widths:  []int{20, 30},
			rows: [][]string{
				{"Item1", "Value1"},
				{"Item2", "Value2"},
			},
			check: func(t *testing.T, result string) {
				t.Helper()
				if !strings.Contains(result, "Name") {
					t.Errorf("Expected Name in header, got: %s", result)
				}
				if !strings.Contains(result, "Value") {
					t.Errorf("Expected Value in header, got: %s", result)
				}
				if !strings.Contains(result, "Item1") {
					t.Errorf("Expected Item1 in result, got: %s", result)
				}
				if !strings.Contains(result, "Value2") {
					t.Errorf("Expected Value2 in result, got: %s", result)
				}
			},
		},
		{
			name:    "mismatched columns and widths",
			headers: []string{"Col1", "Col2", "Col3"},
			widths:  []int{10, 20}, // Only 2 widths for 3 columns
			rows:    [][]string{{"a", "b"}},
			check: func(t *testing.T, result string) {
				t.Helper()
				// Should handle gracefully by using minimum length
				if !strings.Contains(result, "Col1") {
					t.Errorf("Expected Col1 in result, got: %s", result)
				}
				if !strings.Contains(result, "Col2") {
					t.Errorf("Expected Col2 in result, got: %s", result)
				}
				// Col3 should be dropped due to mismatched width
				if strings.Contains(result, "Col3") {
					t.Errorf("Did not expect Col3 in result, got: %s", result)
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			table := NewTable(tt.headers, tt.widths)
			for _, row := range tt.rows {
				table.AddRow(row)
			}
			result := table.Render()
			tt.check(t, result)
		})
	}
}

func TestMockTableRenderer(t *testing.T) {
	tests := []struct {
		name    string
		headers []string
		widths  []int
		rows    [][]string
		want    string
	}{
		{
			name:    "empty mock table",
			headers: []string{"Col1", "Col2"},
			widths:  []int{10, 10},
			rows:    [][]string{},
			want:    "Col1 | Col2\n",
		},
		{
			name:    "mock table with data",
			headers: []string{"Name", "Status"},
			widths:  []int{20, 10},
			rows: [][]string{
				{"Test1", "PASS"},
				{"Test2", "FAIL"},
			},
			want: "Name | Status\nTest1 | PASS\nTest2 | FAIL\n",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mock := NewMockTable(tt.headers, tt.widths)
			for _, row := range tt.rows {
				mock.AddRow(row)
			}

			got := mock.Render()
			if got != tt.want {
				t.Errorf("MockTableRenderer.Render() = %q, want %q", got, tt.want)
			}

			// Test GetHeaders
			headers := mock.GetHeaders()
			if len(headers) != len(tt.headers) {
				t.Errorf("GetHeaders() returned %d headers, want %d", len(headers), len(tt.headers))
			}

			// Test GetRows
			rows := mock.GetRows()
			if len(rows) != len(tt.rows) {
				t.Errorf("GetRows() returned %d rows, want %d", len(rows), len(tt.rows))
			}
		})
	}
}

func TestTableRendererAddRow(t *testing.T) {
	table := NewTable([]string{"Col1", "Col2"}, []int{10, 10})

	// Add multiple rows
	table.AddRow([]string{"a", "b"})
	table.AddRow([]string{"c", "d"})
	table.AddRow([]string{"e", "f"})

	result := table.Render()

	// Check that all rows are present
	for _, val := range []string{"a", "b", "c", "d", "e", "f"} {
		if !strings.Contains(result, val) {
			t.Errorf("Expected %s in rendered table, got: %s", val, result)
		}
	}
}
