package tui

import (
	"context"
	"fmt"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/dncore/wgtun/internal/api"
	"github.com/dncore/wgtun/internal/i18n"
	"github.com/dncore/wgtun/internal/paths"
	"github.com/dncore/wgtun/internal/wire"
)

// stateDoneMsg carries the fetched daemon info plus the local service state
// (which is readable even when the daemon is down).
type stateDoneMsg struct {
	st  *wire.StateInfo
	svc string
}

// stateFailedMsg means the daemon socket is unreachable.
type stateFailedMsg struct {
	err error
	svc string
}

type settingsModel struct {
	client      *api.Client
	lang        i18n.Lang
	langChanged bool
	st          *wire.StateInfo
	svc         string
	offline     bool
}

func newSettings(lang i18n.Lang) *settingsModel {
	return &settingsModel{client: api.Connect(paths.SocketPath), lang: lang}
}

func (m *settingsModel) fetchState() tea.Cmd {
	return async(func() tea.Msg {
		svc := serviceStatus()
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		st, err := m.client.State(ctx)
		if err != nil {
			return stateFailedMsg{err: err, svc: svc}
		}
		return stateDoneMsg{st: st, svc: svc}
	})
}

func (m *settingsModel) Update(msg tea.Msg) (any, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch msg.String() {
		case "l":
			if m.lang == i18n.En {
				m.lang = i18n.Zh
			} else {
				m.lang = i18n.En
			}
			m.langChanged = true
			_ = i18n.Settings{Lang: m.lang}.Save()
			return m, nil
		case "r":
			return m, m.fetchState()
		}
	case stateDoneMsg:
		m.st = msg.st
		m.svc = msg.svc
		m.offline = false
		return m, nil
	case stateFailedMsg:
		m.offline = true
		m.st = nil
		m.svc = msg.svc
		return m, nil
	}
	return m, nil
}

func (m *settingsModel) View(lang i18n.Lang, width int) string {
	var rows []string
	rows = append(rows, titleStyle.Render(i18n.T(lang, i18n.SettingsTitle)))

	// language
	langLabel := i18n.T(lang, i18n.SettingsLang)
	if m.lang == i18n.En {
		langLabel += ": English"
	} else {
		langLabel += ": 中文"
	}
	rows = append(rows, langLabel, subtle.Render("  <l> "+i18n.T(lang, i18n.SettingsLangDesc)))

	rows = append(rows, "", titleStyle.Render(i18n.T(lang, i18n.SettingsDaemon)))
	if m.svc != "" {
		rows = append(rows, "  "+i18n.T(lang, i18n.SettingsService)+": "+subtle.Render(m.svc))
	}
	if m.offline {
		rows = append(rows, "", lipgloss.NewStyle().Foreground(colRed).Render(i18n.T(lang, i18n.SettingsDaemonOff)))
		return lipgloss.JoinVertical(lipgloss.Left, rows...)
	}

	if m.st != nil {
		up := fmt.Sprintf("%dh%dm", int(m.st.UpSec/3600), int(m.st.UpSec%3600/60))
		wgGo := m.st.WireGuardGoPath
		if m.st.WireGuardGoVersion != "" {
			wgGo += "  " + m.st.WireGuardGoVersion
		}
		if !m.st.WireGuardGoOK {
			wgGo = lipgloss.NewStyle().Foreground(colRed).Render(i18n.T(lang, i18n.WgGoMissing))
		} else {
			wgGo = subtle.Render(wgGo)
		}
		health := fmt.Sprintf("healthy (last reconcile %ds ago)", int(m.st.ReconcileAgeSec))
		switch {
		case m.st.LastReconcile.IsZero():
			health = subtle.Render("unknown (daemon is older than this TUI)")
		case !m.st.Healthy:
			health = lipgloss.NewStyle().Foreground(colRed).
				Render(fmt.Sprintf("STALLED (last reconcile %ds ago)", int(m.st.ReconcileAgeSec)))
		}
		rows = append(rows,
			"  "+i18n.T(lang, i18n.SettingsVersion)+": "+m.st.Version,
		)
		if m.st.Backend != "kernel" {
			rows = append(rows, "  "+i18n.T(lang, i18n.SettingsWgGo)+": "+wgGo)
		}
		rows = append(rows,
			"  "+i18n.T(lang, i18n.SettingsBackend)+": "+subtle.Render(m.st.Backend+" "+m.st.BackendDetail),
			"  "+i18n.T(lang, i18n.SettingsResolver)+": "+subtle.Render(m.st.EndpointResolver),
			"  reconcile: "+health,
			"  "+i18n.T(lang, i18n.SettingsUptime)+": "+up,
			"  "+i18n.T(lang, i18n.SettingsSocket)+":  "+subtle.Render(m.st.Socket),
			"  "+i18n.T(lang, i18n.SettingsConfDir)+":  "+subtle.Render(m.st.ConfDir),
			"  "+i18n.T(lang, i18n.SettingsRunDir)+":   "+subtle.Render(m.st.RunDir),
			"  "+i18n.T(lang, i18n.SettingsLogDir)+":   "+subtle.Render(m.st.LogDir),
		)
	} else {
		rows = append(rows, subtle.Render(i18n.T(lang, i18n.SettingsDaemonOff)))
	}
	return lipgloss.JoinVertical(lipgloss.Left, rows...)
}
