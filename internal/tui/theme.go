package tui

import (
	"time"

	"github.com/charmbracelet/lipgloss"
)

// Color palette (works in dark terminals).
var (
	colPrimary   = lipgloss.Color("#7c9fff") // soft blue
	colGreen     = lipgloss.Color("#3fb950")
	colYellow    = lipgloss.Color("#d29922")
	colRed       = lipgloss.Color("#f85149")
	colMuted     = lipgloss.Color("#8b949e")
	colWhite     = lipgloss.Color("#e6edf3")
	colDim       = lipgloss.Color("#484f58")
	colBg        = lipgloss.Color("#0d1117")
	colTableSel  = lipgloss.Color("#161b22")
)

// Base style: black background box.
var base = lipgloss.NewStyle().
	Background(colBg).
	Foreground(colWhite).
	Padding(0, 1)

var (
	titleStyle = lipgloss.NewStyle().Foreground(colPrimary).Bold(true).MarginBottom(1)
	subtle     = lipgloss.NewStyle().Foreground(colMuted)
	statusBar  = lipgloss.NewStyle().Foreground(colWhite).Background(colDim).Padding(0, 1)
	tabActive  = lipgloss.NewStyle().Foreground(colBg).Background(colPrimary).Bold(true).Padding(0, 1)
	tabInactive = lipgloss.NewStyle().Foreground(colMuted).Padding(0, 1)
	footerKey  = lipgloss.NewStyle().Foreground(colPrimary)
	footerDesc = lipgloss.NewStyle().Foreground(colMuted)
	statusText = lipgloss.NewStyle().Foreground(colMuted)
)

// handshakeAgeToColor colors a handshake age:
// fresh (<2min) green, active (<10min) yellow, stale red.
func handshakeAgeToColor(age time.Duration) lipgloss.Color {
	switch {
	case age <= 2*time.Minute:
		return colGreen
	case age <= 10*time.Minute:
		return colYellow
	default:
		return colRed
	}
}

// upDown colors the running/stopped state.
func stateColor(running bool) lipgloss.Color {
	if running {
		return colGreen
	}
	return colMuted
}

// cardStyle is the per-instance dashboard card frame.
var cardStyle = lipgloss.NewStyle().
	Border(lipgloss.RoundedBorder()).
	BorderForeground(colDim).
	Padding(0, 1).
	Width(38)

// cardsContainer lays cards side by side.
var cardsContainer = lipgloss.NewStyle().MarginTop(1)

// tableStyle configures the instances table look.
var tableBase = lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(colDim).Padding(0, 0)