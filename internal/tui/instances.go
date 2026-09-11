package tui

import (
	"context"
	"fmt"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/bubbles/table"
	"github.com/charmbracelet/lipgloss"

	"github.com/dncore/wg-service/internal/api"
	"github.com/dncore/wg-service/internal/i18n"
	"github.com/dncore/wg-service/internal/paths"
	"github.com/dncore/wg-service/internal/wire"
)

// vListenPortClash is a quick heuristic shown in the table; the daemon does
// the authoritative check on save.
func vListenPortClash(v wire.InstanceView) bool { return false }

// instancesModel is the Instances tab: a table of all instances with
// start/stop/edit/delete/new and a detail pane for the selected instance.
type instancesModel struct {
	client       *api.Client
	views        []wire.InstanceView
	table        table.Model
	ready        bool
	needsRefresh bool
	detail      string // serialized status of the selected instance
	confirmName string // instance pending delete confirmation; empty = none
	confirmAct  tea.Cmd
	editing     bool // editor is open
	editor      *editorModel
}

func newInstances() *instancesModel {
	return &instancesModel{client: api.Connect(paths.SocketPath)}
}

func (m *instancesModel) setViews(v []wire.InstanceView) {
	m.views = v
	if m.confirmName != "" || m.editing {
		return
	}
	m.rebuildTable()
}

func (m *instancesModel) rebuildTable() {
	cols := []table.Column{
		{Title: "NAME", Width: 16},
		{Title: "BOOT", Width: 5},
		{Title: "STATE", Width: 9},
		{Title: "PORT", Width: 7},
		{Title: "PEERS", Width: 6},
		{Title: "ONLINE", Width: 7},
		{Title: "UPTIME", Width: 10},
		{Title: "CONFLICT", Width: 14},
	}
	rows := make([]table.Row, 0, len(m.views))
	for _, v := range m.views {
		state := "stopped"
		if v.Running {
			state = "up"
		}
		boot := "·"
		if v.Enabled {
			boot = "★"
		}
		conflict := ""
		if vListenPortClash(v) {
			conflict = "⚠"
		}
		uptime := ""
		if v.Running && v.UptimeSec > 0 {
			uptime = humanDur(time.Duration(v.UptimeSec) * time.Second)
		}
		rows = append(rows, table.Row{
			v.Name, boot, state,
			fmt.Sprint(v.ListenPort), fmt.Sprint(v.PeerCount), fmt.Sprint(v.OnlinePeers),
			uptime, conflict,
		})
	}
	t := table.New(
		table.WithColumns(cols),
		table.WithRows(rows),
		table.WithFocused(true),
		table.WithHeight(12),
	)
	s := table.DefaultStyles()
	s.Header = s.Header.BorderStyle(lipgloss.NormalBorder()).
		BorderForeground(colDim).BorderBottom(true).Bold(false).
		Foreground(colMuted)
	s.Selected = s.Selected.Foreground(colPrimary).Background(colTableSel).Bold(true)
	t.SetStyles(s)
	if m.ready && len(m.table.Rows()) > 0 {
		cur := m.table.Cursor()
		if cur >= len(t.Rows()) {
			cur = len(t.Rows()) - 1
		}
		t.SetCursor(cur)
	}
	m.table = t
	m.ready = true
}

func (m *instancesModel) selected() *wire.InstanceView {
	if len(m.views) == 0 || m.table.Cursor() >= len(m.views) {
		return nil
	}
	return &m.views[m.table.Cursor()]
}

// fetchDetail fetches and renders the live status of the selected instance.
func (m *instancesModel) fetchDetail() tea.Cmd {
	v := m.selected()
	if v == nil {
		return nil
	}
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		resp, err := m.client.Status(ctx, v.Name)
		if err != nil {
			return detailDoneMsg{text: fmt.Sprintf("status: %v", err)}
		}
		if resp.Error != "" {
			return detailDoneMsg{text: resp.Error}
		}
		return detailDoneMsg{text: m.renderDetail(resp)}
	}
}

type detailDoneMsg struct{ text string }

func (m *instancesModel) renderDetail(resp *wire.StatusResp) string {
	d := resp.Status
	if d == nil {
		return ""
	}
	var b string
	for i, p := range d.Peers {
		b += fmt.Sprintf("peer %d  %s\n", i+1, shortKey(p.PublicKey))
		age := "never"
		if !p.LastHandshake.IsZero() {
			age = humanDur(time.Since(p.LastHandshake).Round(time.Second)) + " ago"
		}
		b += fmt.Sprintf("   endpoint %s   keepalive %ds\n", p.Endpoint, p.PersistentKeepalive)
		b += fmt.Sprintf("   handshake %s   rx %s tx %s\n", age, humanBytes(p.RxBytes), humanBytes(p.TxBytes))
		for _, ip := range p.AllowedIPs {
			b += fmt.Sprintf("   allowed-ips %s\n", ip)
		}
	}
	return b
}

func shortKey(k string) string {
	if len(k) > 12 {
		return k[:6] + "…" + k[len(k)-6:]
	}
	return k
}

func (m *instancesModel) Update(msg tea.Msg) (any, tea.Cmd) {
	if m.editing {
		next, cmd := m.editor.Update(msg)
		if ed, ok := next.(*editorModel); ok && ed.done {
			m.editing = false
			m.needsRefresh = true
			return m, tea.Batch(cmd, m.refreshCmd())
		}
		return m, cmd
	}

	switch msg := msg.(type) {
	case tea.KeyMsg:
		// confirmation dialog is modal
		if m.confirmName != "" {
			return m.updateConfirm(msg)
		}
		switch msg.String() {
		case "u", "d":
			v := m.selected()
			if v == nil {
				return m, nil
			}
			action := "up"
			if v.Running {
				action = "down"
			}
			return m, m.actionCmd(action, v)
		case "r":
			v := m.selected()
			if v == nil {
				return m, nil
			}
			return m, m.actionCmd("restart", v)
		case "b":
			v := m.selected()
			if v == nil {
				return m, nil
			}
			return m, m.toggleBootCmd(v)
		case "e":
			v := m.selected()
			if v == nil {
				return m, nil
			}
			m.editing = true
			m.editor = newEditor(m.client, v.Name, false)
			return m, m.editor.load()
		case "n":
			m.editing = true
			m.editor = newEditor(m.client, "", true)
			return m, nil
		case "x":
			v := m.selected()
			if v == nil {
				return m, nil
			}
			m.confirmName = v.Name
			m.confirmAct = m.deleteCmd(v)
			return m, nil
		case "enter":
			return m, m.fetchDetail()
		case "c":
			return m, nil
		}
	case detailDoneMsg:
		m.detail = msg.text
		return m, nil
	}
	return m, nil
}

func (m *instancesModel) refreshCmd() tea.Cmd {
	return func() tea.Msg { return refreshMsg{} }
}

func (m *instancesModel) actionCmd(act string, v *wire.InstanceView) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		var err error
		switch act {
		case "up":
			err = m.client.Up(ctx, v.Name)
		case "down":
			err = m.client.Down(ctx, v.Name)
		case "restart":
			err = m.client.Restart(ctx, v.Name)
		}
		switch {
		case act == "up" && err == nil:
			return statusMsg{key: i18n.Started}
		case act == "down" && err == nil:
			return statusMsg{key: i18n.Stopped}
		case act == "restart" && err == nil:
			return statusMsg{key: i18n.Restarted}
		default:
			return statusMsg{key: i18n.ActionFailed, args: []any{err}}
		}
	}
}

func (m *instancesModel) toggleBootCmd(v *wire.InstanceView) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := m.client.SetEnabled(ctx, v.Name, !v.Enabled); err != nil {
			return statusMsg{key: i18n.ActionFailed, args: []any{err}}
		}
		if !v.Enabled {
			return statusMsg{key: i18n.BootEnabled}
		}
		return statusMsg{key: i18n.BootDisabled}
	}
}

func (m *instancesModel) deleteCmd(v *wire.InstanceView) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := m.client.Delete(ctx, v.Name, false); err != nil {
			return statusMsg{key: i18n.ActionFailed, args: []any{err}}
		}
		return statusMsg{key: i18n.Deleted}
	}
}

func (m *instancesModel) updateConfirm(msg tea.KeyMsg) (any, tea.Cmd) {
	switch msg.String() {
	case "y":
		m.confirmName = ""
		if m.confirmAct != nil {
			c := m.confirmAct
			m.confirmAct = nil
			return m, tea.Batch(c, m.refreshCmd())
		}
		return m, nil
	case "n", "esc":
		m.confirmName = ""
		m.confirmAct = nil
		return m, nil
	}
	return m, nil
}

func (m *instancesModel) View(lang i18n.Lang, width int) string {
	if m.editing {
		return m.editor.View(lang, width)
	}
	if !m.ready {
		return "…"
	}
	var b []string
	b = append(b, titleStyle.Render(i18n.T(lang, i18n.InstTitle)))
	b = append(b, m.table.View())
	if m.detail != "" {
		b = append(b, "", lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(colDim).Padding(0, 1).Render(m.detail))
	}
	if m.confirmName != "" {
		b = append(b, "", lipgloss.NewStyle().Foreground(colYellow).Render(i18n.T(lang, i18n.ConfirmDelete, m.confirmName)))
	}
	return lipgloss.JoinVertical(lipgloss.Left, b...)
}