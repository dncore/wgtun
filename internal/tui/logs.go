package tui

import (
	"context"
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/bubbles/viewport"
	"github.com/charmbracelet/lipgloss"

	"github.com/dncore/wg-service/internal/api"
	"github.com/dncore/wg-service/internal/i18n"
	"github.com/dncore/wg-service/internal/logs"
	"github.com/dncore/wg-service/internal/paths"
)

// logsModel is the Logs tab: history with filters plus live-follow mode.
type logsModel struct {
	client *api.Client

	events   []logs.Event
	viewport viewport.Model
	ready    bool

	follow    bool
	followCh  chan logs.Event
	followCtx context.CancelFunc

	instances map[string]bool // known instance names for the filter
	instance  string          // "" = all
	minLevel  logs.Level
	text      string
}

func newLogs() *logsModel {
	return &logsModel{
		client:    api.Connect(paths.SocketPath),
		instances: map[string]bool{},
		minLevel:  logs.Debug,
	}
}

// setKnown syncs the instance names used by the filter cycle.
func (m *logsModel) setKnown(names []string) {
	m.instances = map[string]bool{}
	for _, n := range names {
		m.instances[n] = true
	}
}

type logsDataMsg struct {
	events []logs.Event
	err    error
}

func (m *logsModel) fetch() tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		events, err := m.client.LogsQuery(ctx, logs.Filter{
			Instance: m.instance,
			MinLevel: m.minLevel,
			Text:     m.text,
			Limit:    300,
		})
		return logsDataMsg{events: events, err: err}
	}
}

// startFollow opens the follow stream: a goroutine pumps events into a
// channel, and each tea.Cmd takes exactly one event back into the pipeline.
func (m *logsModel) startFollow() tea.Cmd {
	ctx, cancel := context.WithCancel(context.Background())
	m.followCtx = cancel
	m.follow = true
	m.events = nil
	m.followCh = make(chan logs.Event, 128)
	client := m.client
	f := logs.Filter{Instance: m.instance, MinLevel: m.minLevel, Text: m.text}
	go func() {
		defer close(m.followCh)
		client.LogsFollow(ctx, f, func(ev logs.Event) error {
			select {
			case m.followCh <- ev:
				return nil
			default:
				return fmt.Errorf("slow consumer")
			}
		})
	}()
	return m.takeFollow()
}

// takeFollow blocks for the next event on the follow channel.
func (m *logsModel) takeFollow() tea.Cmd {
	return func() tea.Msg {
		ev, ok := <-m.followCh
		if !ok {
			return logsFollowMsg{err: fmt.Errorf("stream ended")}
		}
		return logsFollowMsg{ev: ev}
	}
}

func (m *logsModel) stopFollow() {
	if m.followCtx != nil {
		m.followCtx()
		m.followCtx = nil
	}
	m.follow = false
}

type logsFollowMsg struct {
	ev  logs.Event
	err error
}

func (m *logsModel) Update(msg tea.Msg) (any, tea.Cmd) {
	switch msg := msg.(type) {
	case logsDataMsg:
		if msg.err != nil {
			return m, nil
		}
		m.events = msg.events
		m.renderViewport()
		return m, nil
	case logsFollowMsg:
		if msg.err != nil {
			m.stopFollow()
			return m, nil
		}
		m.appendEvent(msg.ev)
		if m.follow {
			return m, m.takeFollow() // keep consuming
		}
		return m, nil
	case tea.KeyMsg:
		switch msg.String() {
		case "f":
			if m.follow {
				m.stopFollow()
				return m, m.fetch()
			}
			return m, m.startFollow()
		case "c":
			m.instance = ""
			m.minLevel = logs.Debug
			m.text = ""
			m.stopFollow()
			return m, m.fetch()
		case "i":
			m.stopFollow()
			m.cycleInstance()
			return m, m.fetch()
		case "l":
			m.stopFollow()
			m.cycleLevel()
			return m, m.fetch()
		}
		if m.follow {
			return m, nil // ignore scroll keys while following
		}
		var cmd tea.Cmd
		m.viewport, cmd = m.viewport.Update(msg)
		return m, cmd
	}
	return m, nil
}

func (m *logsModel) cycleInstance() {
	names := make([]string, 0, len(m.instances))
	for n := range m.instances {
		names = append(names, n)
	}
	if len(names) == 0 {
		m.instance = ""
		return
	}
	idx := -1
	for i, n := range names {
		if n == m.instance {
			idx = i
			break
		}
	}
	if idx < 0 || idx >= len(names)-1 {
		m.instance = ""
	} else {
		m.instance = names[idx+1]
	}
}

func (m *logsModel) cycleLevel() {
	switch m.minLevel {
	case logs.Debug:
		m.minLevel = logs.Warn
		m.instance = ""
	case logs.Warn:
		m.minLevel = logs.Error
	case logs.Error:
		m.minLevel = logs.Debug
	}
}

func (m *logsModel) renderViewport() {
	var b strings.Builder
	for _, ev := range m.events {
		b.WriteString(formatEvent(ev))
		b.WriteString("\n")
	}
	if len(m.events) == 0 {
		b.WriteString(i18n.T(i18n.En, i18n.LogsNoEvents))
	}
	if !m.ready {
		m.viewport = viewport.New(80, 18)
		m.ready = true
	}
	m.viewport.SetContent(b.String())
}

func (m *logsModel) appendEvent(ev logs.Event) {
	if m.filterMatch(ev) {
		m.events = append(m.events, ev)
		m.renderViewport()
	}
}

func (m *logsModel) filterMatch(ev logs.Event) bool {
	if m.instance != "" && ev.Instance != m.instance {
		return false
	}
	if ev.Level < m.minLevel {
		return false
	}
	if m.text != "" && !strings.Contains(strings.ToLower(ev.Msg), strings.ToLower(m.text)) {
		return false
	}
	return true
}

func (m *logsModel) View(lang i18n.Lang, width int) string {
	var b []string
	title := i18n.T(lang, i18n.LogsTitle)
	if m.follow {
		title += "  [" + i18n.T(lang, i18n.LogsFollow) + "]"
	}
	b = append(b, titleStyle.Render(title))

	// filter line
	inst := m.instance
	if inst == "" {
		inst = i18n.T(lang, i18n.LogsAll)
	}
	level := m.minLevel.String()
	filterLine := fmt.Sprintf("  inst:%s  level:%s  text:%q  [i] inst  [l] level  [f] %s  [c] clear",
		inst, level, m.text, i18n.T(lang, i18n.KeyFollow))
	b = append(b, subtle.Render(filterLine))

	if !m.ready {
		b = append(b, "…")
		return lipgloss.JoinVertical(lipgloss.Left, b...)
	}
	b = append(b, m.viewport.View())
	return lipgloss.JoinVertical(lipgloss.Left, b...)
}

// formatEvent renders one log line with level colors.
func formatEvent(ev logs.Event) string {
	ts := ev.Time.Format("15:04:05")
	level := lipgloss.NewStyle().Foreground(levelColor(ev.Level)).Render(pad(ev.Level.String(), 5))
	inst := ev.Instance
	if inst == "" {
		inst = "-"
	}
	return fmt.Sprintf("%s  %s  %-14s %s", ts, level, inst, ev.Msg)
}

func levelColor(l logs.Level) lipgloss.Color {
	switch l {
	case logs.Debug:
		return colMuted
	case logs.Info:
		return colPrimary
	case logs.Warn:
		return colYellow
	case logs.Error:
		return colRed
	}
	return colMuted
}

func pad(s string, n int) string {
	for len(s) < n {
		s += " "
	}
	return s
}

var _ = time.Second