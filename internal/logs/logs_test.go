package logs

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func newTestStore(t *testing.T) *Store {
	t.Helper()
	s, err := New(filepath.Join(t.TempDir(), "events.jsonl"), 100, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestRingQueryFilters(t *testing.T) {
	s := newTestStore(t)
	s.Info("wg0", "started")
	s.Error("wg1", "endpoint resolve failed: vpn.example.com")
	s.Info("", "daemon up")
	s.Warn("wg0", "restart backoff")

	got := s.Query(Filter{})
	if len(got) != 4 {
		t.Fatalf("want 4 events, got %d", len(got))
	}
	// newest first
	if got[0].Msg != "restart backoff" {
		t.Fatalf("newest first violated: %v", got[0].Msg)
	}

	if n := len(s.Query(Filter{Instance: "wg0"})); n != 2 {
		t.Fatalf("instance filter: %d", n)
	}
	if n := len(s.Query(Filter{MinLevel: Warn})); n != 2 {
		t.Fatalf("level filter: %d", n)
	}
	if n := len(s.Query(Filter{Text: "RESOLVE"})); n != 1 {
		t.Fatalf("text filter (case-insensitive): %d", n)
	}
	if n := len(s.Query(Filter{Limit: 2})); n != 2 {
		t.Fatalf("limit: %d", n)
	}
}

func TestRingWraparound(t *testing.T) {
	dir := t.TempDir()
	s, err := New(filepath.Join(dir, "e.jsonl"), 5, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 12; i++ {
		s.Info("wg0", "event %02d", i)
	}
	got := s.Query(Filter{})
	if len(got) != 5 {
		t.Fatalf("ring must cap at 5, got %d", len(got))
	}
	if got[0].Msg != "event 11" || got[4].Msg != "event 07" {
		t.Fatalf("wraparound lost events: %v .. %v", got[0].Msg, got[4].Msg)
	}
}

func TestFilePersistAndQuery(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "e.jsonl")
	s, err := New(path, 10, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	s.Info("wg0", "alpha")
	s.Error("wg1", "beta")

	// a fresh store over the same file must read history back
	s2, err := New(path, 10, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	hist := s2.QueryFile(Filter{})
	if len(hist) != 2 {
		t.Fatalf("want 2 persisted events, got %d", len(hist))
	}
	if hist[0].Msg != "beta" { // newest first
		t.Fatalf("order wrong: %v", hist[0].Msg)
	}
	if hist[1].Level != Info || hist[1].Instance != "wg0" {
		t.Fatalf("fields lost: %+v", hist[1])
	}
}

func TestFileQueryLimitKeepsNewest(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "e.jsonl")
	s, err := New(path, 100, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 10; i++ {
		s.Info("wg0", "event %02d", i)
	}
	s2, err := New(path, 100, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	got := s2.QueryFile(Filter{Limit: 3})
	if len(got) != 3 {
		t.Fatalf("limit: got %d events", len(got))
	}
	// A limited history query must be the newest slice, newest first —
	// returning the oldest N makes a long-running log view look frozen.
	if got[0].Msg != "event 09" || got[2].Msg != "event 07" {
		t.Fatalf("limit must keep the newest events, got %v .. %v", got[0].Msg, got[2].Msg)
	}
}

func TestRotation(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "e.jsonl")
	// tiny cap to force rotation
	s, err := New(path, 1000, 4000)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 200; i++ {
		s.Info("wg0", "%s", strings.Repeat("x", 60))
	}
	st, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if st.Size() > 20000 { // rotation clearly failed
		t.Fatalf("file grew to %d bytes without rotation", st.Size())
	}
	// the remaining file must still be valid JSONL
	hist := s.QueryFile(Filter{})
	if len(hist) == 0 {
		t.Fatal("rotation lost all events")
	}
	for _, ev := range hist {
		if !ev.Time.After(time.Time{}) {
			t.Fatal("corrupt event after rotation")
		}
	}
}

func TestSubscribe(t *testing.T) {
	s := newTestStore(t)
	ch, cancel := s.Subscribe()
	defer cancel()
	s.Info("wg0", "hello")
	select {
	case ev := <-ch:
		if ev.Msg != "hello" {
			t.Fatalf("got %v", ev.Msg)
		}
	case <-time.After(time.Second):
		t.Fatal("no event on subscription")
	}
	cancel()
	// cancel closes the channel
	if _, open := <-ch; open {
		t.Fatal("channel not closed after cancel")
	}
}
