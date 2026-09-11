package tui

import (
	"strings"
	"testing"
)

func TestSparkRowsShape(t *testing.T) {
	rows := sparkRows([]float64{0, 1, 2, 4}, 6, 2)
	if len(rows) != 2 {
		t.Fatalf("want 2 rows, got %d", len(rows))
	}
	for _, r := range rows {
		if len([]rune(r)) != 6 {
			t.Fatalf("row width = %d want 6: %q", len([]rune(r)), r)
		}
	}
	// right-aligned: the first two columns (no samples yet) stay blank
	for _, r := range rows {
		if !strings.HasPrefix(r, "  ") {
			t.Fatalf("row not right-aligned: %q", r)
		}
	}
	// the max-value column must be full height in both rows
	for _, r := range rows {
		if []rune(r)[5] != '█' {
			t.Fatalf("max column not full: %q", r)
		}
	}
	// the zero sample must be empty
	if []rune(rows[1])[2] != ' ' {
		t.Fatalf("zero sample should render blank: %q", rows[1])
	}
}

func TestSparkRowsTruncatesToWidth(t *testing.T) {
	vals := []float64{1, 2, 3, 4, 5, 6, 7, 8, 9}
	rows := sparkRows(vals, 4, 3)
	if len(rows) != 3 {
		t.Fatalf("rows = %d", len(rows))
	}
	for _, r := range rows {
		if len([]rune(r)) != 4 {
			t.Fatalf("row width = %d: %q", len([]rune(r)), r)
		}
	}
	// newest sample (9) is the max and must be full height
	for _, r := range rows {
		if []rune(r)[3] != '█' {
			t.Fatalf("newest column not full: %q", r)
		}
	}
}

func TestSparkRowsAllZero(t *testing.T) {
	rows := sparkRows([]float64{0, 0, 0}, 5, 2)
	for _, r := range rows {
		if strings.TrimSpace(r) != "" {
			t.Fatalf("all-zero series should render blank: %q", r)
		}
	}
}