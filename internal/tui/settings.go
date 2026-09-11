package tui

import (
	"context"
	"fmt"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/dncore/wg-service/internal/api"
	"github.com/dncore/wg-service/internal/i18n"
	"github.com/dncore/wg-service/internal/paths"
	"github.com/dncore/wg-service/internal/wire"
)

// stateDoneMsg carries the fetched daemon info.
type stateDoneMsg struct{ st *wire.StateInfo }

// stateFailedMsg means the daemon socket is unreachable.
type stateFailedMsg struct{ err error }

type settingsModel struct {
	client      *api.Client
	lang        i18n.Lang
	langChanged bool
	st          *wire.StateInfo
	offline     bool
}

func newSettings(lang i18n.Lang) *settingsModel {
	return &settingsModel{client: api.Connect(paths.SocketPath), lang: lang}
}

func (m *settingsModel) fetchState() tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		st, err := m.client.State(ctx)
		if err != nil {
			return stateFailedMsg{err: err}
		}
		return stateDoneMsg{st: st}
	}
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
		m.offline = false
		return m, nil
	case stateFailedMsg:
		m.offline = true
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

	if m.offline {
		rows = append(rows, "", lipgloss.NewStyle().Foreground(colRed).Render(i18n.T(lang, i18n.SettingsDaemonOff)))
		return lipgloss.JoinVertical(lipgloss.Left, rows...)
	}

	rows = append(rows, "", titleStyle.Render(i18n.T(lang, i18n.SettingsDaemon)))
	if m.st != nil {
		up := fmt.Sprintf("%dh%dm", int(m.st.UpSec/3600), int(m.st.UpSec%3600/60))
		rows = append(rows,
			"  "+i18n.T(lang, i18n.SettingsVersion)+": "+m.st.Version,
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