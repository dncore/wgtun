package tui

import (
	"fmt"
	"reflect"
	"regexp"
	"slices"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/dncore/wgtun/internal/wire"
)

func testViews() []wire.InstanceView {
	return []wire.InstanceView{
		{Name: "alpha", ListenPort: 51820},
		{Name: "bravo", ListenPort: 51821},
		{Name: "charlie", ListenPort: 51822},
	}
}

func keyMsg(t tea.KeyType) tea.KeyMsg { return tea.KeyMsg{Type: t} }

func runeMsg(r rune) tea.KeyMsg { return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}} }

func TestInstancesArrowKeysMoveSelection(t *testing.T) {
	m := newInstances()
	m.setViews(testViews())
	if got := m.selected().Name; got != "alpha" {
		t.Fatalf("initial selection = %q, want alpha", got)
	}
	m.Update(keyMsg(tea.KeyDown))
	if got := m.selected().Name; got != "bravo" {
		t.Fatalf("after ↓ selection = %q, want bravo", got)
	}
	m.Update(keyMsg(tea.KeyDown))
	if got := m.selected().Name; got != "charlie" {
		t.Fatalf("after ↓↓ selection = %q, want charlie", got)
	}
	m.Update(keyMsg(tea.KeyUp))
	if got := m.selected().Name; got != "bravo" {
		t.Fatalf("after ↓↓↑ selection = %q, want bravo", got)
	}
	// the cursor must not run past the ends
	for i := 0; i < 4; i++ {
		m.Update(keyMsg(tea.KeyUp))
	}
	if got := m.selected().Name; got != "alpha" {
		t.Fatalf("after 4× ↑ selection = %q, want alpha (clamped)", got)
	}
}

func TestInstancesVimKeysMoveSelection(t *testing.T) {
	m := newInstances()
	m.setViews(testViews())
	m.Update(runeMsg('j'))
	if got := m.selected().Name; got != "bravo" {
		t.Fatalf("after j selection = %q, want bravo", got)
	}
	m.Update(runeMsg('k'))
	if got := m.selected().Name; got != "alpha" {
		t.Fatalf("after jk selection = %q, want alpha", got)
	}
}

// u/d/b are the start/stop/boot actions; they must not double as table
// movement (bubbles' default keymap binds them to half-page scrolls).
func TestInstancesActionKeysDoNotMoveCursor(t *testing.T) {
	m := newInstances()
	m.setViews(testViews())
	for _, r := range []rune{'d', 'u', 'b'} {
		_, cmd := m.Update(runeMsg(r))
		if cmd == nil {
			t.Fatalf("%q: want an action command, got nil", r)
		}
		if got := m.selected().Name; got != "alpha" {
			t.Fatalf("%q moved the selection to %q", r, got)
		}
	}
}

// The detail pane shows the live status of one instance; once the cursor
// moves it must not keep rendering the previous instance's peers.
func TestInstancesCursorMoveClearsStaleDetail(t *testing.T) {
	m := newInstances()
	m.setViews(testViews())
	m.detail = "alpha status"
	m.Update(keyMsg(tea.KeyDown))
	if m.detail != "" || m.detailResp != nil {
		t.Fatalf("detail pane kept stale content after moving the cursor: %q", m.detail)
	}
	// a key press that does not move the cursor leaves the pane alone
	for i := 0; i < 3; i++ {
		m.Update(keyMsg(tea.KeyUp)) // park on the first row
	}
	m.detail = "alpha status"
	m.Update(keyMsg(tea.KeyUp))
	if m.detail == "" {
		t.Fatal("detail pane cleared on a no-op cursor move")
	}
}

// manyViews returns n instances, more than the table's 12 rows so that it
// has to scroll.
func manyViews(n int, uptime int64) []wire.InstanceView {
	v := make([]wire.InstanceView, 0, n)
	for i := 0; i < n; i++ {
		v = append(v, wire.InstanceView{
			Name:       fmt.Sprintf("i%02d", i),
			Running:    true,
			UptimeSec:  uptime,
			ListenPort: 51820 + i,
		})
	}
	return v
}

var (
	ansiRE = regexp.MustCompile("\x1b\\[[0-9;]*[a-zA-Z]")
	nameRE = regexp.MustCompile(`^i[0-9]{2}$`)
)

// visibleNames is the NAME column of the rows the table actually renders.
func visibleNames(m *instancesModel) []string {
	var names []string
	for _, line := range strings.Split(m.table.View(), "\n") {
		f := strings.Fields(ansiRE.ReplaceAllString(line, ""))
		if len(f) > 0 && nameRE.MatchString(f[0]) {
			names = append(names, f[0])
		}
	}
	return names
}

func scrollToLast(t *testing.T, m *instancesModel, name string) {
	t.Helper()
	for i := 0; i < 40 && m.selected().Name != name; i++ {
		m.Update(keyMsg(tea.KeyDown))
	}
	if got := m.selected().Name; got != name {
		t.Fatalf("selection = %q, want %q", got, name)
	}
}

// The poll runs every few seconds and almost always brings a changed cell
// (the uptime column ticks). It must not move the visible window.
func TestInstancesPollKeepsScrollPosition(t *testing.T) {
	m := newInstances()
	m.setViews(manyViews(20, 100))
	scrollToLast(t, m, "i19")

	before := visibleNames(m)
	if !slices.Contains(before, "i19") {
		t.Fatalf("selected row is not rendered: %v", before)
	}
	m.setViews(manyViews(20, 200)) // same instances, uptime ticked
	after := visibleNames(m)
	if !slices.Contains(after, "i19") {
		t.Fatalf("poll scrolled the selected row out of view: %v", after)
	}
	if !reflect.DeepEqual(before, after) {
		t.Fatalf("poll moved the table view:\n before %v\n after  %v", before, after)
	}
	if got := m.selected().Name; got != "i19" {
		t.Fatalf("poll moved the selection to %q", got)
	}
}

// Deleting instances from under a scrolled cursor must leave a usable table:
// no blank body, no cursor pointing past the end.
func TestInstancesShrinkKeepsTableUsable(t *testing.T) {
	m := newInstances()
	m.setViews(manyViews(20, 100))
	scrollToLast(t, m, "i19")

	m.setViews(manyViews(3, 100))
	if got := m.selected().Name; got != "i02" {
		t.Fatalf("selection after shrink = %q, want i02", got)
	}
	if names := visibleNames(m); !slices.Contains(names, "i02") {
		t.Fatalf("selected row not rendered after shrink: %v", names)
	}

	// every instance gone, then one created again
	m.setViews(nil)
	if m.selected() != nil {
		t.Fatal("selection survived an empty instance list")
	}
	if m.table.View() == "" {
		t.Fatal("empty table rendered nothing at all")
	}
	m.setViews(manyViews(1, 100))
	if got := m.selected(); got == nil || got.Name != "i00" {
		t.Fatalf("selection after refill = %v, want i00", got)
	}
	if names := visibleNames(m); !slices.Contains(names, "i00") {
		t.Fatalf("refilled table does not render the instance: %v", names)
	}
}
