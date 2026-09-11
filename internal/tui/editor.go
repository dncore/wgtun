package tui

import (
	"context"
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/bubbles/textinput"
	"github.com/charmbracelet/lipgloss"

	"github.com/dncore/wg-service/internal/api"
	"github.com/dncore/wg-service/internal/i18n"
	"github.com/dncore/wg-service/internal/wgconf"
)

// peerDraft is the editable state of one peer.
type peerDraft struct {
	publicKey string
	psk       string
	allowed   string
	endpoint  string
	keepalive string
}

// editorModel is a modal form for creating/editing one instance.
type editorModel struct {
	client *api.Client
	name   string // instance name (fixed when editing)
	isNew  bool

	// interface form fields, in order: name, key, address, port, mtu, dns
	fields []textinput.Model
	focus  int

	// peers
	peers    []*peerDraft
	peerMode bool // peer list visible instead of interface form
	peerSel  int
	// per-peer field edit
	peerEdit bool
	peerDraftFields []textinput.Model
	peerFieldIdx    int

	err  string
	busy bool
	done bool
}

func newEditor(client *api.Client, name string, isNew bool) *editorModel {
	e := &editorModel{client: client, name: name, isNew: isNew}
	mk := func(v, ph string) textinput.Model {
		t := textinput.New()
		t.SetValue(v)
		t.Placeholder = ph
		return t
	}
	keyVal := ""
	if isNew {
		if k, err := wgconf.GeneratePrivateKey(); err == nil {
			keyVal = k
		}
	}
	e.fields = []textinput.Model{
		mk(name, "instance name"), // 0 name
		mk(keyVal, "private key"), // 1 key
		mk("", "203.0.113.1/24"),  // 2 address
		mk("", "51820"),           // 3 listen port
		mk("", "1420"),            // 4 mtu
		mk("", "8.8.8.8"),         // 5 dns
	}
	e.fields[0].Focus()
	return e
}

// load fetches the config of an existing instance into the form.
func (e *editorModel) load() tea.Cmd {
	if e.isNew {
		return nil
	}
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		content, err := e.client.Conf(ctx, e.name)
		if err != nil {
			return editorLoadMsg{err: err}
		}
		return editorLoadMsg{content: content}
	}
}

type editorLoadMsg struct {
	content string
	err     error
}

func (e *editorModel) applyConf(text string) {
	mode := "iface"
	peers := []*peerDraft{}
	var cur *peerDraft
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		switch strings.ToLower(line) {
		case "[interface]":
			mode = "iface"
			continue
		case "[peer]":
			mode = "peer"
			cur = &peerDraft{}
			peers = append(peers, cur)
			continue
		}
		key, val, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		key, val = strings.TrimSpace(key), strings.TrimSpace(val)
		if mode == "iface" {
			switch strings.ToLower(key) {
			case "privatekey":
				e.fields[1].SetValue(val)
			case "address":
				e.fields[2].SetValue(val)
			case "listenport":
				e.fields[3].SetValue(val)
			case "mtu":
				e.fields[4].SetValue(val)
			case "dns":
				e.fields[5].SetValue(val)
			}
		} else if cur != nil {
			switch strings.ToLower(key) {
			case "publickey":
				cur.publicKey = val
			case "presharedkey":
				cur.psk = val
			case "allowedips":
				cur.allowed = strings.Join(strings.FieldsFunc(val, func(r rune) bool { return r == ',' }), ", ")
			case "endpoint":
				cur.endpoint = val
			case "persistentkeepalive":
				cur.keepalive = val
			}
		}
	}
	e.peers = peers
}

func (e *editorModel) serialize() string {
	var b strings.Builder
	b.WriteString("[Interface]\n")
	if v := e.fields[1].Value(); v != "" {
		b.WriteString("PrivateKey = " + v + "\n")
	}
	if v := e.fields[2].Value(); v != "" {
		b.WriteString("Address = " + v + "\n")
	}
	if v := e.fields[5].Value(); v != "" {
		b.WriteString("DNS = " + v + "\n")
	}
	if v := e.fields[3].Value(); v != "" {
		b.WriteString("ListenPort = " + v + "\n")
	}
	if v := e.fields[4].Value(); v != "" {
		b.WriteString("MTU = " + v + "\n")
	}
	for _, p := range e.peers {
		b.WriteString("\n[Peer]\n")
		if p.publicKey != "" {
			b.WriteString("PublicKey = " + p.publicKey + "\n")
		}
		if p.psk != "" {
			b.WriteString("PresharedKey = " + p.psk + "\n")
		}
		if p.allowed != "" {
			b.WriteString("AllowedIPs = " + p.allowed + "\n")
		}
		if p.endpoint != "" {
			b.WriteString("Endpoint = " + p.endpoint + "\n")
		}
		if p.keepalive != "" {
			b.WriteString("PersistentKeepalive = " + p.keepalive + "\n")
		}
	}
	return b.String()
}

func (e *editorModel) save(force bool) tea.Cmd {
	if e.isNew {
		e.name = strings.TrimSpace(e.fields[0].Value())
	}
	content := e.serialize()
	e.busy = true
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
		defer cancel()
		var err error
		if e.isNew {
			err = e.client.Create(ctx, e.name, content, force)
		} else {
			err = e.client.Update(ctx, e.name, content, force)
		}
		if err != nil {
			return editorSaveMsg{err: err}
		}
		return editorSaveMsg{ok: true}
	}
}

type editorSaveMsg struct {
	ok  bool
	err error
}

func (e *editorModel) Update(msg tea.Msg) (any, tea.Cmd) {
	switch msg := msg.(type) {
	case editorLoadMsg:
		if msg.err != nil {
			e.err = msg.err.Error()
			return e, nil
		}
		e.applyConf(msg.content)
		return e, nil
	case editorSaveMsg:
		if msg.err != nil {
			e.err = msg.err.Error()
			e.busy = false
			return e, nil
		}
		e.err = ""
		e.done = true
		return e, statusCmd(i18n.EditorSaved)
	}

	km, ok := msg.(tea.KeyMsg)
	if !ok {
		return e, nil
	}
	// global save/cancel keys work in every editor mode
	switch km.String() {
	case "ctrl+s", "ctrl+o":
		return e, e.save(false)
	case "ctrl+f":
		return e, e.save(true)
	case "esc":
		switch {
		case e.peerEdit:
			e.peerEdit = false
		case e.peerMode:
			e.peerMode = false
		default:
			e.done = true
		}
		return e, nil
	}

	switch {
	case e.peerEdit:
		return e.updatePeerField(km)
	case e.peerMode:
		return e.updatePeerList(km)
	default:
		return e.updateIface(km)
	}
}

func (e *editorModel) updateIface(km tea.KeyMsg) (any, tea.Cmd) {
	switch km.Type {
	case tea.KeyDown, tea.KeyEnter, tea.KeyTab:
		if km.Type == tea.KeyEnter && e.focus == len(e.fields)-1 {
			e.peerMode = true
			return e, nil
		}
		e.setFocus(e.focus + 1)
		return e, nil
	case tea.KeyUp:
		e.setFocus(e.focus - 1)
		return e, nil
	}
	if e.focus == 1 && km.String() == "g" {
		if k, err := wgconf.GeneratePrivateKey(); err == nil {
			e.fields[1].SetValue(k)
		}
		return e, nil
	}
	nf, cmd := e.fields[e.focus].Update(km)
	e.fields[e.focus] = nf
	return e, cmd
}

func (e *editorModel) setFocus(n int) {
	if n < 0 || n >= len(e.fields) {
		return
	}
	e.fields[e.focus].Blur()
	e.focus = n
	e.fields[n].Focus()
}

func (e *editorModel) updatePeerList(km tea.KeyMsg) (any, tea.Cmd) {
	switch km.String() {
	case "n":
		e.peers = append(e.peers, &peerDraft{})
		e.peerSel = len(e.peers) - 1
		return e, nil
	case "d":
		if len(e.peers) > 0 {
			e.peers = append(e.peers[:e.peerSel], e.peers[e.peerSel+1:]...)
			if e.peerSel >= len(e.peers) {
				e.peerSel = len(e.peers) - 1
			}
		}
		return e, nil
	case "e", "enter":
		if len(e.peers) == 0 {
			return e, nil
		}
		p := e.peers[e.peerSel]
		mk := func(v, ph string) textinput.Model {
			t := textinput.New()
			t.SetValue(v)
			t.Placeholder = ph
			return t
		}
		e.peerDraftFields = []textinput.Model{
			mk(p.publicKey, "PublicKey"),
			mk(p.allowed, "AllowedIPs"),
			mk(p.endpoint, "Endpoint host:port"),
			mk(p.keepalive, "Keepalive (s)"),
			mk(p.psk, "PresharedKey"),
		}
		e.peerFieldIdx = 0
		e.peerDraftFields[0].Focus()
		e.peerEdit = true
		return e, nil
	case "up":
		if e.peerSel > 0 {
			e.peerSel--
		}
		return e, nil
	case "down":
		if e.peerSel < len(e.peers)-1 {
			e.peerSel++
		}
		return e, nil
	}
	return e, nil
}

func (e *editorModel) updatePeerField(km tea.KeyMsg) (any, tea.Cmd) {
	switch km.Type {
	case tea.KeyEnter, tea.KeyTab:
		if e.peerFieldIdx == len(e.peerDraftFields)-1 {
			// write back to the peer
			p := e.peers[e.peerSel]
			p.publicKey = e.peerDraftFields[0].Value()
			p.allowed = e.peerDraftFields[1].Value()
			p.endpoint = e.peerDraftFields[2].Value()
			p.keepalive = e.peerDraftFields[3].Value()
			p.psk = e.peerDraftFields[4].Value()
			e.peerEdit = false
			return e, nil
		}
		e.peerDraftFields[e.peerFieldIdx].Blur()
		e.peerFieldIdx++
		e.peerDraftFields[e.peerFieldIdx].Focus()
		return e, nil
	}
	nf, cmd := e.peerDraftFields[e.peerFieldIdx].Update(km)
	e.peerDraftFields[e.peerFieldIdx] = nf
	return e, cmd
}

func (e *editorModel) View(lang i18n.Lang, width int) string {
	var b []string
	title := i18n.T(lang, i18n.EditorTitle)
	if e.isNew {
		title = i18n.T(lang, i18n.EditorNewTitle)
	}
	b = append(b, titleStyle.Render(title))

	switch {
	case e.peerEdit:
		labels := []string{
			i18n.T(lang, i18n.FieldPeerPublic),
			i18n.T(lang, i18n.FieldPeerAllowed),
			i18n.T(lang, i18n.FieldPeerEndpoint),
			i18n.T(lang, i18n.FieldPeerKeepalive),
			i18n.T(lang, i18n.FieldPeerPSK),
		}
		for i, f := range e.peerDraftFields {
			lbl := labels[i]
			if i == 0 {
				lbl += "  [g] generate"
			}
			row := lbl + "\n" + f.View()
			if i == e.peerFieldIdx {
				row = lipgloss.NewStyle().Foreground(colPrimary).Render(row)
			}
			b = append(b, row, "")
		}
	case e.peerMode:
		b = append(b, subtle.Render(i18n.T(lang, i18n.Peers)))
		for i, p := range e.peers {
			mark := "  "
			if i == e.peerSel {
				mark = "▶ "
			}
			b = append(b, fmt.Sprintf("%s%s  %s", mark, shortKey(p.publicKey), p.endpoint))
		}
		if len(e.peers) == 0 {
			b = append(b, subtle.Render(i18n.T(lang, i18n.NoPeers)))
		}
		b = append(b, "", subtle.Render(i18n.T(lang, i18n.PeerListHint)))
	default:
		labels := []string{
			i18n.T(lang, i18n.FieldName),
			i18n.T(lang, i18n.FieldPrivateKey),
			i18n.T(lang, i18n.FieldAddress),
			i18n.T(lang, i18n.FieldListenPort),
			i18n.T(lang, i18n.FieldMTU),
			i18n.T(lang, i18n.FieldDNS),
		}
		for i, f := range e.fields {
			lbl := labels[i]
			if i == 1 {
				lbl += "  [g] " + i18n.T(lang, i18n.KeyGenKey)
			}
			row := lbl + "\n" + f.View()
			if i == e.focus {
				row = lipgloss.NewStyle().Foreground(colPrimary).Render(row)
			}
			b = append(b, row, "")
		}
		b = append(b, subtle.Render(i18n.T(lang, i18n.KeyAddPeer)+" → enter"))
	}
	if e.err != "" {
		b = append(b, "", lipgloss.NewStyle().Foreground(colRed).Render(i18n.T(lang, i18n.ConflictFound, e.err)))
	}
	b = append(b, "", subtle.Render(
		i18n.T(lang, i18n.KeySave)+" ctrl+s   "+
			i18n.T(lang, i18n.KeyForce)+" ctrl+f   "+
			i18n.T(lang, i18n.KeyCancel)+" esc"))
	return lipgloss.JoinVertical(lipgloss.Left, b...)
}