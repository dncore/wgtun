// TUI main application: tab container, refresh polling, status line.
package tui

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/dncore/wg-service/internal/api"
	"github.com/dncore/wg-service/internal/i18n"
	"github.com/dncore/wg-service/internal/paths"
	"github.com/dncore/wg-service/internal/wire"
)

// Run starts the TUI. It exits when the user quits.
func Run() {
	app := newApp()
	p := tea.NewProgram(app, tea.WithAltScreen())
	if _, err := p.Run(); err != nil {
		fmt.Fprintln(os.Stderr, "wgs:", err)
		os.Exit(1)
	}
}

type tabID int

const (
	tabDashboard tabID = iota
	tabInstances
	tabLogs
	tabSettings
)

// tickMsg triggers the periodic refresh poll.
type tickMsg time.Time

// appMsg wraps a sub-model message so it dispatches to the active tab.
type appMsg struct{ dst tabID; msg tea.Msg }

// statusMsg sets the one-line status text; rendered through i18n at display
// time so the current language always applies.
type statusMsg struct {
	key  string
	args []any
}

// refreshMsg asks the app to re-poll instance state.
type refreshMsg struct{}

type app struct {
	lang    i18n.Lang
	tab     tabID
	client  *api.Client
	ctx     context.Context
	cancel  context.CancelFunc

	// per-tab sub-models
	dash    *dashboardModel
	inst    *instancesModel
	logs    *logsModel
	settings *settingsModel

	status   string
	busy     bool
	offline  bool

	width, height int
}

func newApp() *app {
	ctx, cancel := context.WithCancel(context.Background())
	lang := i18n.LoadSettings().Lang
	return &app{
		lang:   lang,
		client: api.Connect(paths.SocketPath),
		ctx:    ctx,
		cancel: cancel,
		dash:   newDashboard(),
		inst:   newInstances(),
		logs:   newLogs(),
		settings: newSettings(lang),
		status: i18n.T(lang, i18n.StatusBusy),
	}
}

func (a *app) Init() tea.Cmd {
	return tea.Batch(
		tea.Tick(2*time.Second, func(t time.Time) tea.Msg { return tickMsg(t) }),
		a.poll(),
	)
}

// poll fetches instances + state and refreshes the current tab.
func (a *app) poll() tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(a.ctx, 3*time.Second)
		defer cancel()
		views, err := a.client.Instances(ctx)
		if err != nil {
			a.offline = true
			return pollDoneMsg{err: err}
		}
		a.offline = false
		return pollDoneMsg{views: views}
	}
}

type pollDoneMsg struct {
	views []wire.InstanceView
	date  time.Time
	err   error
}

func (a *app) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch m := msg.(type) {
	case tea.WindowSizeMsg:
		a.width, a.height = m.Width, m.Height
		return a, nil

	case tea.KeyMsg:
		switch m.Type {
		case tea.KeyTab, tea.KeyShiftTab:
			dir := 1
			if m.Type == tea.KeyShiftTab {
				dir = -1
			}
			a.tab = tabID((int(a.tab) + dir + 4) % 4)
			a.inst.table.Focus()
			return a, a.poll()
		case tea.KeyCtrlQ, tea.KeyCtrlC:
			a.cancel()
			return a, tea.Quit
		}
		// forward to the active tab
		return a.forward(tea.KeyMsg(m))

	case tickMsg:
		return a, tea.Batch(tea.Tick(2*time.Second, func(t time.Time) tea.Msg { return tickMsg(t) }), a.poll())

	case pollDoneMsg:
		if m.err != nil {
			a.status = i18n.T(a.lang, i18n.DaemonOffline)
			a.dash.setOffline(true)
			return a, nil
		}
		a.dash.setOffline(false)
		a.dash.setViews(m.views)
		a.inst.setViews(m.views)
		// fetch per-instance live status for the dashboard
		var cmds []tea.Cmd
		for _, v := range m.views {
			if v.Running {
				cmds = append(cmds, a.dash.fetchStatusCmd(a.client, v.Name))
			}
		}
		return a, tea.Batch(cmds...)

	case statusMsg:
		a.status = i18n.T(a.lang, m.key, m.args...)
		a.busy = false
		return a, nil

	case refreshMsg:
		return a, a.poll()

	case appMsg:
		a.forward(m.msg)
		return a, nil

	default:
		return a.forward(msg)
	}
}

// forward dispatches a message to the active tab model.
func (a *app) forward(msg tea.Msg) (tea.Model, tea.Cmd) {
	var cmds []tea.Cmd
	refresh := false
	switch a.tab {
	case tabDashboard:
		_, cmd := a.dash.Update(msg)
		cmds = append(cmds, cmd)
	case tabInstances:
		_, cmd := a.inst.Update(msg)
		cmds = append(cmds, cmd)
		refresh = a.inst.needsRefresh
		a.inst.needsRefresh = false
	case tabLogs:
		_, cmd := a.logs.Update(msg)
		cmds = append(cmds, cmd)
	case tabSettings:
		_, cmd := a.settings.Update(msg)
		cmds = append(cmds, cmd)
		if a.settings.langChanged {
			a.lang = a.settings.lang
			a.status = i18n.T(a.lang, i18n.StatusDone)
			a.settings.langChanged = false
		}
	}
	if refresh {
		cmds = append(cmds, a.poll())
	}
	return a, tea.Batch(cmds...)
}

func (a *app) View() string {
	title := i18n.T(a.lang, i18n.DashboardTitle)
	if a.offline {
		title += "  (" + i18n.T(a.lang, i18n.DaemonOffline) + ")"
	}
	var body string
	switch a.tab {
	case tabDashboard:
		body = a.dash.View(a.lang, a.width)
	case tabInstances:
		body = a.inst.View(a.lang, a.width)
	case tabLogs:
		body = a.logs.View(a.lang, a.width)
	case tabSettings:
		body = a.settings.View(a.lang, a.width)
	}

	tabs := a.renderTabs()
	status := a.status
	if a.busy {
		status = i18n.T(a.lang, i18n.StatusBusy)
	}
	// footer keys
	footer := a.renderFooter()

	content := lipgloss.JoinVertical(lipgloss.Left,
		tabs,
		body,
		"",
		statusBar.Width(a.width).Render(" "+status+" "+footer),
	)
	return base.Width(a.width).Render(content)
}

func (a *app) renderTabs() string {
	names := []string{
		i18n.T(a.lang, i18n.TabDashboard),
		i18n.T(a.lang, i18n.TabInstances),
		i18n.T(a.lang, i18n.TabLogs),
		i18n.T(a.lang, i18n.TabSettings),
	}
	var b strings.Builder
	for i, n := range names {
		st := tabInactive
		if tabID(i) == a.tab {
			st = tabActive
		}
		b.WriteString(st.Render(" " + n + " "))
	}
	return b.String()
}

func (a *app) renderFooter() string {
	var keys []string
	switch a.tab {
	case tabInstances:
		keys = append(keys,
			key(i18n.T(a.lang, i18n.KeyStartStop), "u/d"),
			key(i18n.T(a.lang, i18n.KeyEdit), "e"),
			key(i18n.T(a.lang, i18n.KeyDelete), "x"),
			key(i18n.T(a.lang, i18n.KeyNew), "n"),
			key(i18n.T(a.lang, i18n.KeyToggleBoot), "b"),
			key(i18n.T(a.lang, i18n.KeyDetails), "enter"),
		)
	case tabLogs:
		keys = append(keys,
			key(i18n.T(a.lang, i18n.KeyFollow), "f"),
			key(i18n.T(a.lang, i18n.KeyClearFilter), "c"),
		)
	case tabSettings:
		keys = append(keys,
			key(i18n.T(a.lang, i18n.KeySwitchLang), "l"),
		)
	case tabDashboard:
		keys = append(keys, key(i18n.T(a.lang, i18n.KeyRefresh), "r"))
	}
	keys = append(keys, key(i18n.T(a.lang, i18n.KeyQuit), "ctrl+q"))
	var b strings.Builder
	for _, k := range keys {
		b.WriteString(k)
		b.WriteString("   ")
	}
	return b.String()
}

func key(desc, k string) string {
	return footerKey.Render(k) + " " + footerDesc.Render(desc)
}

// statusCmd wraps an i18n key into a statusMsg cmd.
func statusCmd(key string, args ...any) tea.Cmd {
	return func() tea.Msg { return statusMsg{key: key, args: args} }
}