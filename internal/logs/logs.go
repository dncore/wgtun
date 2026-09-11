// Package logs provides the daemon event log: an in-memory ring buffer,
// an append-only JSONL file with size-based rotation, filtering queries and
// live subscription for the TUI follow mode.
package logs

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// Level of an event.
type Level int

const (
	Debug Level = iota
	Info
	Warn
	Error
)

func (l Level) String() string {
	switch l {
	case Debug:
		return "DEBUG"
	case Info:
		return "INFO"
	case Warn:
		return "WARN"
	case Error:
		return "ERROR"
	}
	return "?"
}

// ParseLevel maps a level name (case-insensitive) to a Level.
func ParseLevel(s string) (Level, error) {
	switch strings.ToLower(s) {
	case "debug":
		return Debug, nil
	case "info", "":
		return Info, nil
	case "warn", "warning":
		return Warn, nil
	case "error":
		return Error, nil
	}
	return Info, fmt.Errorf("unknown level %q", s)
}

// Event is one structured log line.
type Event struct {
	Time     time.Time `json:"time"`
	Level    Level     `json:"level"`
	Instance string    `json:"instance,omitempty"`
	Msg      string    `json:"msg"`
}

// Store is the event log. Safe for concurrent use.
type Store struct {
	mu   sync.Mutex
	ring []Event // fixed-capacity circular buffer
	head int     // next write position
	n    int     // current count

	path     string
	maxSize  int64
	subs     map[int]chan Event
	nextSub  int
	lastErr  error
}

// New opens (creating if needed) the JSONL event log at path and keeps a
// ring of ringSize events in memory. maxSize is the rotation threshold.
func New(path string, ringSize int, maxSize int64) (*Store, error) {
	if ringSize <= 0 {
		ringSize = 10000
	}
	if maxSize <= 0 {
		maxSize = 5 << 20
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	s := &Store{
		ring:    make([]Event, ringSize),
		path:    path,
		maxSize: maxSize,
		subs:    make(map[int]chan Event),
	}
	return s, nil
}

// Log appends an event.
func (s *Store) Log(l Level, instance, msg string, args ...any) {
	ev := Event{
		Time:     time.Now(),
		Level:    l,
		Instance: instance,
		Msg:      fmt.Sprintf(msg, args...),
	}
	s.mu.Lock()
	s.ring[s.head] = ev
	s.head = (s.head + 1) % len(s.ring)
	if s.n < len(s.ring) {
		s.n++
	}
	s.appendLocked(ev)
	for _, ch := range s.subs {
		select {
		case ch <- ev:
		default: // slow consumer: drop rather than block the daemon
		}
	}
	s.mu.Unlock()
}

// Convenience helpers.
func (s *Store) Info(instance, msg string, args ...any)  { s.Log(Info, instance, msg, args...) }
func (s *Store) Warn(instance, msg string, args ...any)  { s.Log(Warn, instance, msg, args...) }
func (s *Store) Error(instance, msg string, args ...any) { s.Log(Error, instance, msg, args...) }
func (s *Store) Debug(instance, msg string, args ...any) { s.Log(Debug, instance, msg, args...) }

// appendLocked writes the JSONL line and rotates when over the size cap.
// Caller must hold s.mu.
func (s *Store) appendLocked(ev Event) {
	f, err := os.OpenFile(s.path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o640)
	if err != nil {
		s.setErrLocked(err)
		return
	}
	line, _ := json.Marshal(ev)
	f.Write(append(line, '\n'))
	st, _ := f.Stat()
	f.Close()
	if st != nil && st.Size() > s.maxSize {
		s.rotateLocked(st.Size())
	}
}

// rotateLocked keeps the newest half of the file (by bytes, cut at line
// boundaries). Caller must hold s.mu.
func (s *Store) rotateLocked(size int64) {
	data, err := os.ReadFile(s.path)
	if err != nil {
		s.setErrLocked(err)
		return
	}
	cut := size / 2
	// advance to the next newline so we keep whole lines
	for cut < int64(len(data)) && data[cut] != '\n' {
		cut++
	}
	if cut < int64(len(data)) {
		cut++ // skip the newline itself
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, data[cut:], 0o640); err != nil {
		s.setErrLocked(err)
		return
	}
	if err := os.Rename(tmp, s.path); err != nil {
		s.setErrLocked(err)
		return
	}
}

func (s *Store) setErrLocked(err error) { s.lastErr = err }

// LastErr returns the most recent logging error (mostly for diagnostics).
func (s *Store) LastErr() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.lastErr
}

// Filter selects events for Query.
type Filter struct {
	Instance string // exact match, empty = all
	MinLevel Level   // events with level >= MinLevel
	Since    time.Time
	Until    time.Time
	Text     string // case-insensitive substring on Msg
	Limit    int    // max events returned, newest first; 0 = no limit
}

// Query returns events from the ring buffer, newest first.
func (s *Store) Query(f Filter) []Event {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []Event
	for i := 0; i < s.n; i++ {
		idx := (s.head - 1 - i + len(s.ring)*2) % len(s.ring)
		ev := s.ring[idx]
		if !f.Match(ev) {
			continue
		}
		out = append(out, ev)
		if f.Limit > 0 && len(out) >= f.Limit {
			break
		}
	}
	return out
}

// QueryFile returns events from the persisted JSONL file, newest last but
// returned newest-first for consistency with Query. It merges nothing —
// callers wanting full history then live tail use this plus Subscribe.
func (s *Store) QueryFile(f Filter) []Event {
	s.mu.Lock()
	path := s.path
	s.mu.Unlock()
	fh, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer fh.Close()
	var out []Event
	sc := bufio.NewScanner(fh)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		var ev Event
		if json.Unmarshal(sc.Bytes(), &ev) != nil {
			continue
		}
		if !f.Match(ev) {
			continue
		}
		out = append(out, ev)
	}
	// reverse to newest-first
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	if f.Limit > 0 && len(out) > f.Limit {
		out = out[len(out)-f.Limit:]
	}
	return out
}

// Match reports whether an event passes the filter.
func (f Filter) Match(ev Event) bool {
	if f.Instance != "" && ev.Instance != f.Instance {
		return false
	}
	if ev.Level < f.MinLevel {
		return false
	}
	if !f.Since.IsZero() && ev.Time.Before(f.Since) {
		return false
	}
	if !f.Until.IsZero() && ev.Time.After(f.Until) {
		return false
	}
	if f.Text != "" && !strings.Contains(strings.ToLower(ev.Msg), strings.ToLower(f.Text)) {
		return false
	}
	return true
}

// Subscribe returns a channel receiving future events and a cancel func.
// The channel is buffered; slow consumers drop events.
func (s *Store) Subscribe() (<-chan Event, func()) {
	s.mu.Lock()
	defer s.mu.Unlock()
	ch := make(chan Event, 256)
	id := s.nextSub
	s.nextSub++
	s.subs[id] = ch
	return ch, func() {
		s.mu.Lock()
		defer s.mu.Unlock()
		if c, ok := s.subs[id]; ok {
			delete(s.subs, id)
			close(c)
		}
	}
}
