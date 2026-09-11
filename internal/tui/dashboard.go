package tui

import (
	"context"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/dncore/wg-service/internal/api"
	"github.com/dncore/wg-service/internal/i18n"
	"github.com/dncore/wg-service/internal/uapi"
	"github.com/dncore/wg-service/internal/wire"
)

// dashSample keeps per-instance throughput history for sparklines.
// Samples are throughput deltas (bytes/s) between consecutive polls, not the
// cumulative byte counters — a cumulative curve only ever rises and jumps to
// zero when the tunnel restarts, which tells you nothing about activity.
type dashSample struct {
	rx      []float64
	curRate float64 // most recent rx rate, for the caption
	lastRx  uint64  // cumulative rx at the previous sample
	lastAt  time.Time
	lastSet bool
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
	if resp.Status == nil {
		return
	}
	var rx uint64
	for _, p := range resp.Status.Peers {
		rx += p.RxBytes
	}
	now := time.Now()
	if hist.lastSet {
		dt := now.Sub(hist.lastAt).Seconds()
		if dt >= 0.5 {
			rate := 0.0
			if rx >= hist.lastRx { // a decrease means the tunnel restarted
				rate = float64(rx-hist.lastRx) / dt
			}
			hist.rx = append(hist.rx, rate)
			if len(hist.rx) > 30 {
				hist.rx = hist.rx[1:]
			}
			hist.curRate = rate
		}
	}
	hist.lastRx = rx
	hist.lastAt = now
	hist.lastSet = true
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

	// per-instance cards, two per row; every card in a row is padded to the
	// tallest one so the layout never jumps as data refreshes
	const perRow = 2
	var cardRows []string
	for i := 0; i < len(m.views); i += perRow {
		end := i + perRow
		if end > len(m.views) {
			end = len(m.views)
		}
		var cards []string
		maxH := 0
		for _, v := range m.views[i:end] {
			c := m.renderCard(lang, v)
			if h := lipgloss.Height(c); h > maxH {
				maxH = h
			}
			cards = append(cards, c)
		}
		for j := range cards {
			if lipgloss.Height(cards[j]) < maxH {
				cards[j] = lipgloss.NewStyle().Height(maxH).Render(cards[j])
			}
		}
		cardRows = append(cardRows, lipgloss.JoinHorizontal(lipgloss.Top, cards...))
	}
	b = append(b, cardsContainer.Render(lipgloss.JoinVertical(lipgloss.Left, cardRows...)))
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

// cardWidth is the inner text width available inside cardStyle.
const cardWidth = 34

// peerLines is the fixed row count per peer block; the card height only
// depends on the (stable) peer count, never on which fields have data.
const peerLines = 4

func (m *dashboardModel) renderCard(lang i18n.Lang, v wire.InstanceView) string {
	resp := m.statuses[v.Name]
	var dev *uapi.DeviceStatus
	if resp != nil {
		dev = resp.Status
	}

	state := i18n.T(lang, i18n.InstStopped)
	c := stateColor(false)
	if v.Running {
		state = i18n.T(lang, i18n.InstRunning)
		c = stateColor(true)
	}
	tun := v.Tun
	if tun == "" {
		tun = "-"
	}
	rows := []string{fmt.Sprintf("%s  %s  %s",
		lipgloss.NewStyle().Bold(true).Render(v.Name),
		lipgloss.NewStyle().Foreground(c).Render(state),
		subtle.Render(tun))}

	// interface line: listen port + derived device public key
	listen := "-"
	pub := "-"
	if dev != nil && dev.ListenPort != 0 {
		listen = strconv.Itoa(dev.ListenPort)
	}
	if resp != nil && resp.DevicePublicKey != "" {
		pub = shortKey(resp.DevicePublicKey)
	}
	rows = append(rows, fmt.Sprintf("%s %s  %s %s",
		subtle.Render(i18n.T(lang, i18n.CardListen)), listen,
		subtle.Render(i18n.T(lang, i18n.CardPubKey)), pub))

	// peer blocks, fixed height each
	var peers []uapi.PeerStatus
	if dev != nil {
		peers = dev.Peers
	}
	for i := 0; i < v.PeerCount; i++ {
		if i < len(peers) {
			rows = append(rows, m.peerRows(lang, i, peers[i])...)
		} else {
			// placeholder keeps the height stable while data is loading
			rows = append(rows, fmt.Sprintf("● peer %d", i+1))
			for j := 1; j < peerLines; j++ {
				rows = append(rows, "")
			}
		}
	}

	// sparkline area, fixed 5 rows (4 chart + 1 caption)
	if hist, ok := m.history[v.Name]; ok && len(hist.rx) > 2 {
		rows = append(rows, sparkRows(hist.rx, cardWidth, 4)...)
		rows = append(rows, subtle.Render(fmt.Sprintf("rx %s/s", humanRate(hist.curRate))))
	} else {
		for i := 0; i < 5; i++ {
			rows = append(rows, "")
		}
	}
	return cardStyle.Render(lipgloss.JoinVertical(lipgloss.Left, rows...))
}

// sparkRows renders a column sparkline of the newest values, right-aligned
// into a width×height grid. Each column is quantized to eighths via block
// characters. Hand-rolled instead of asciigraph because that library draws
// long horizontal ledges between sparse samples and shifts its axis labels
// as the auto-scale changes.
func sparkRows(vals []float64, width, height int) []string {
	grid := make([][]rune, height)
	for r := range grid {
		grid[r] = []rune(strings.Repeat(" ", width))
	}
	if len(vals) > width {
		vals = vals[len(vals)-width:]
	}
	offset := width - len(vals)

	maxV := 0.0
	for _, v := range vals {
		if v > maxV {
			maxV = v
		}
	}
	if maxV <= 0 {
		maxV = 1
	}

	blocks := []rune("▁▂▃▄▅▆▇█")
	for col, v := range vals {
		// level in eighths of a cell, over the full height
		level := int(math.Round(v / maxV * float64(height*8)))
		for r := 0; r < height; r++ {
			full := (height - r) * 8 // eighths needed to fill this cell
			switch {
			case level >= full:
				grid[r][offset+col] = '█'
			case level > full-8:
				grid[r][offset+col] = blocks[level-(full-8)-1]
			}
		}
	}
	out := make([]string, height)
	for r := range grid {
		out[r] = string(grid[r])
	}
	return out
}

// peerRows renders exactly peerLines rows for one peer.
// Truncation only ever applies to plain text: cutting a styled string would
// slice through an ANSI escape and garble the output.
func (m *dashboardModel) peerRows(lang i18n.Lang, idx int, p uapi.PeerStatus) []string {
	suffixPlain := ""
	if p.HasPSK {
		suffixPlain += " PSK"
	}
	if p.PersistentKeepalive > 0 {
		suffixPlain += fmt.Sprintf(" ka%d", p.PersistentKeepalive)
	}
	head := truncateRunes(fmt.Sprintf("● peer %d  %s", idx+1, shortKey(p.PublicKey)),
		cardWidth-len([]rune(suffixPlain)))
	if p.HasPSK {
		head += " " + lipgloss.NewStyle().Foreground(colGreen).Render("PSK")
	}
	if p.PersistentKeepalive > 0 {
		head += " " + subtle.Render(fmt.Sprintf("ka%d", p.PersistentKeepalive))
	}

	endpoint := p.Endpoint
	if endpoint == "" {
		endpoint = i18n.T(lang, i18n.CardNone)
	}
	allowed := strings.Join(p.AllowedIPs, ", ")
	if allowed == "" {
		allowed = i18n.T(lang, i18n.CardNone)
	}

	hs := i18n.T(lang, i18n.Never)
	switch {
	case p.LastHandshake.IsZero():
	case time.Since(p.LastHandshake) <= 2*time.Minute:
		hs = lipgloss.NewStyle().Foreground(colGreen).Render(i18n.T(lang, i18n.HandshakeNow))
	default:
		age := time.Since(p.LastHandshake).Round(time.Second)
		hs = lipgloss.NewStyle().Foreground(handshakeAgeToColor(age)).Render(i18n.T(lang, i18n.HandshakeAgo, humanDur(age)))
	}

	return []string{
		head,
		"  " + subtle.Render("endpoint ") + truncateRunes(endpoint, cardWidth-11),
		"  " + subtle.Render("allowed  ") + truncateRunes(allowed, cardWidth-11),
		"  " + hs + subtle.Render("  rx "+humanBytes(p.RxBytes)+" tx "+humanBytes(p.TxBytes)),
	}
}

// truncateRunes shortens s to at most n display runes.
func truncateRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n || n < 2 {
		return s
	}
	return string(r[:n-1]) + "…"
}

// humanRate formats a bytes-per-second value.
func humanRate(bps float64) string {
	switch {
	case bps < 1024:
		return fmt.Sprintf("%.0fB", bps)
	case bps < 1024*1024:
		return fmt.Sprintf("%.1fKB", bps/1024)
	default:
		return fmt.Sprintf("%.1fMB", bps/(1024*1024))
	}
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