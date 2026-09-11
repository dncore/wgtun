package tui

import (
	"context"
	"fmt"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/guptarohit/asciigraph"

	"github.com/dncore/wg-service/internal/api"
	"github.com/dncore/wg-service/internal/i18n"
	"github.com/dncore/wg-service/internal/wire"
)

// dashSample keeps per-instance throughput history for sparklines.
// plot caches the rendered sparkline so View() never re-runs asciigraph.
type dashSample struct {
	rx   []float64
	tx   []float64
	plot string
}

type dashboardModel struct {
	views    []wire.InstanceView
	statuses map[string]*wire.StatusResp // name → last status fetch
	history  map[string]*dashSample
	offline  bool
}

func newDashboard() *dashboardModel {
	return &dashboardModel{
		statuses: map[string]*wire.StatusResp{},
		history:  map[string]*dashSample{},
	}
}

func (m *dashboardModel) setViews(v []wire.InstanceView) { m.views = v; m.offline = false }
func (m *dashboardModel) setOffline(o bool)              { m.offline = o }

// statusDoneMsg carries the status of one instance, fetched by the main poll.
type statusDoneMsg struct {
	name string
	resp *wire.StatusResp
}

func (m *dashboardModel) updateStatus(name string, resp *wire.StatusResp) {
	m.statuses[name] = resp
	hist, ok := m.history[name]
	if !ok {
		hist = &dashSample{}
		m.history[name] = hist
	}
	if resp.Status != nil {
		var rx uint64
		for _, p := range resp.Status.Peers {
			rx += p.RxBytes
		}
		hist.rx = append(hist.rx, float64(rx))
		if len(hist.rx) > 30 {
			hist.rx = hist.rx[1:]
		}
		if len(hist.rx) > 2 {
			hist.plot = asciigraph.Plot(hist.rx, asciigraph.Height(4), asciigraph.Width(34), asciigraph.Caption("rx"))
		}
	}
}

func (m *dashboardModel) Update(msg tea.Msg) (any, tea.Cmd) {
	switch msg := msg.(type) {
	case statusDoneMsg:
		m.updateStatus(msg.name, msg.resp)
		return m, nil
	}
	return m, nil
}

// fetchStatusCmd returns a cmd to fetch one instance's live status.
func (m *dashboardModel) fetchStatusCmd(client *api.Client, name string) tea.Cmd {
	return async(func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		resp, err := client.Status(ctx, name)
		if err != nil {
			return statusDoneMsg{name: name, resp: &wire.StatusResp{Error: err.Error()}}
		}
		return statusDoneMsg{name: name, resp: resp}
	})
}

func (m *dashboardModel) View(lang i18n.Lang, width int) string {
	var b []string
	b = append(b, titleStyle.Render(i18n.T(lang, i18n.DashboardTitle)))

	if m.offline {
		b = append(b, lipgloss.NewStyle().Foreground(colRed).Render(i18n.T(lang, i18n.DaemonOffline)))
		return lipgloss.JoinVertical(lipgloss.Left, b...)
	}
	if len(m.views) == 0 {
		b = append(b, subtle.Render(i18n.T(lang, i18n.NoInstances)))
		return lipgloss.JoinVertical(lipgloss.Left, b...)
	}

	// summary stats
	running, online := 0, 0
	var rx, tx uint64
	for _, v := range m.views {
		if v.Running {
			running++
		}
		online += v.OnlinePeers
		if st := m.statuses[v.Name]; st != nil && st.Status != nil {
			for _, p := range st.Status.Peers {
				rx += p.RxBytes
				tx += p.TxBytes
			}
		}
	}
	stats := lipgloss.JoinHorizontal(lipgloss.Top,
		statTile(i18n.T(lang, i18n.StatRunning), fmt.Sprintf("%d/%d", running, len(m.views)), stateColor(running == len(m.views))),
		statTile(i18n.T(lang, i18n.StatOnline), fmt.Sprint(online), colPrimary),
		statTile(i18n.T(lang, i18n.StatTraffic), fmt.Sprintf("%s ↓ / %s ↑", humanBytes(rx), humanBytes(tx)), colMuted),
	)
	b = append(b, stats)

	// per-instance cards, two per row
	var cards []string
	for _, v := range m.views {
		cards = append(cards, m.renderCard(lang, v))
	}
	b = append(b, cardsContainer.Render(lipgloss.JoinHorizontal(lipgloss.Top, cards...)))
	return lipgloss.JoinVertical(lipgloss.Left, b...)
}

func statTile(label, value string, color lipgloss.Color) string {
	return lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(colDim).
		Padding(0, 2).
		Render(lipgloss.JoinVertical(lipgloss.Left,
			subtle.Render(label),
			lipgloss.NewStyle().Foreground(color).Bold(true).Render(value),
		))
}

func (m *dashboardModel) renderCard(lang i18n.Lang, v wire.InstanceView) string {
	state := i18n.T(lang, i18n.InstStopped)
	c := stateColor(false)
	if v.Running {
		state = i18n.T(lang, i18n.InstRunning)
		c = stateColor(true)
	}
	head := fmt.Sprintf("%s  %s",
		lipgloss.NewStyle().Bold(true).Render(v.Name),
		lipgloss.NewStyle().Foreground(c).Render(state))
	rows := []string{head}

	if st := m.statuses[v.Name]; st != nil && st.Status != nil {
		dev := st.Status
		// newest handshake across peers
		var newest time.Time
		for _, p := range dev.Peers {
			if p.LastHandshake.After(newest) {
				newest = p.LastHandshake
			}
		}
		var ageStr string
		switch {
		case newest.IsZero():
			ageStr = i18n.T(lang, i18n.Never)
		case time.Since(newest) <= 2*time.Minute:
			ageStr = lipgloss.NewStyle().Foreground(colGreen).Render(i18n.T(lang, i18n.HandshakeNow))
		default:
			age := time.Since(newest).Round(time.Second)
			ageStr = lipgloss.NewStyle().Foreground(handshakeAgeToColor(age)).Render(i18n.T(lang, i18n.HandshakeAgo, humanDur(age)))
		}
		rows = append(rows, subtle.Render(i18n.T(lang, i18n.CardHandshake)+": ")+ageStr)

		var rx, tx uint64
		online := 0
		cut := time.Now().Add(-3 * time.Minute)
		for _, p := range dev.Peers {
			rx += p.RxBytes
			tx += p.TxBytes
			if p.LastHandshake.After(cut) {
				online++
			}
		}
		rows = append(rows,
			subtle.Render(i18n.T(lang, i18n.CardRx)+" "+humanBytes(rx)+"  "+
				subtle.Render(i18n.T(lang, i18n.CardTx)+" "+humanBytes(tx))),
			fmt.Sprintf("%s %d/%d  %s %s",
				subtle.Render(i18n.T(lang, i18n.CardPeers)), online, len(dev.Peers),
				subtle.Render(i18n.T(lang, i18n.CardTun)), v.Tun),
		)
	} else if v.Running {
		rows = append(rows, subtle.Render(i18n.T(lang, i18n.CardTun)+": "+v.Tun))
	}

	if hist, ok := m.history[v.Name]; ok && hist.plot != "" {
		rows = append(rows, hist.plot)
	}
	return cardStyle.Render(lipgloss.JoinVertical(lipgloss.Left, rows...))
}

func humanBytes(n uint64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%dB", n)
	}
	div, exp := uint64(unit), 0
	for m := n / unit; m >= unit; m /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f%c", float64(n)/float64(div), "KMGTPE"[exp])
}

func humanDur(d time.Duration) string {
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	default:
		return fmt.Sprintf("%dh%dm", int(d.Hours()), int(d.Minutes())%60)
	}
}