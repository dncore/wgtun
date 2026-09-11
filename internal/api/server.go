// Package api serves the daemon control API over a Unix socket (plain
// HTTP/JSON so it is curl-able for debugging) and provides the client the
// TUI uses.
package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/user"
	"strconv"
	"time"

	"github.com/dncore/wg-service/internal/logs"
	"github.com/dncore/wg-service/internal/paths"
	"github.com/dncore/wg-service/internal/uapi"
	"github.com/dncore/wg-service/internal/wire"
)

// Supervisor is the daemon surface the API needs.
type Supervisor interface {
	Views() []wire.InstanceView
	LiveStatus(name string) (*uapi.DeviceStatus, time.Time, error)
	Conf(name string) (string, error)
	Start(name string) error
	Stop(name string) error
	Restart(name string) error
	SetEnabled(name string, enabled bool) error
	CreateInstance(name, content string, force bool) error
	UpdateInstance(name, content string, force bool) error
	DeleteInstance(name string, force bool) error
}

// Logger is the event-log surface the API needs.
type Logger interface {
	Query(logs.Filter) []logs.Event
	QueryFile(logs.Filter) []logs.Event
	Subscribe() (<-chan logs.Event, func())
}

// Server is the running API server.
type Server struct {
	http *http.Server
	ln   net.Listener
}

// Serve starts the API on the given unix socket path. Access control is the
// socket permission (root:admin 0660).
func Serve(sup Supervisor, ev Logger, sockPath string) (*Server, error) {
	if err := os.Remove(sockPath); err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	ln, err := net.Listen("unix", sockPath)
	if err != nil {
		return nil, err
	}
	// group-writable for the admin group so the unprivileged TUI can connect
	if err := os.Chown(sockPath, 0, adminGid()); err == nil {
		os.Chmod(sockPath, 0o660)
	}

	srv := &Server{ln: ln}
	started := time.Now()

	mux := http.NewServeMux()
	mux.HandleFunc("GET /state", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, wire.StateInfo{
			Version: version, Started: started, Socket: sockPath,
			ConfDir: paths.ConfDir, RunDir: paths.RunDir, LogDir: paths.LogDir,
			UpSec: int64(time.Since(started).Seconds()),
		})
	})
	mux.HandleFunc("GET /instances", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, sup.Views())
	})
	mux.HandleFunc("GET /instances/{name}/status", func(w http.ResponseWriter, r *http.Request) {
		name := r.PathValue("name")
		st, at, err := sup.LiveStatus(name)
		resp := wire.StatusResp{}
		for _, v := range sup.Views() {
			if v.Name == name {
				resp.View = v
			}
		}
		if err != nil {
			resp.Error = err.Error()
		} else {
			resp.Status = st
			resp.StatusAge = time.Since(at).Seconds()
		}
		writeJSON(w, resp)
	})
	mux.HandleFunc("GET /instances/{name}/conf", func(w http.ResponseWriter, r *http.Request) {
		content, err := sup.Conf(r.PathValue("name"))
		if err != nil {
			writeErr(w, http.StatusNotFound, err)
			return
		}
		writeJSON(w, map[string]string{"content": content})
	})
	mux.HandleFunc("POST /instances", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Name    string `json:"name"`
			Content string `json:"content"`
			Force   bool   `json:"force"`
		}
		if !readJSON(w, r, &req) {
			return
		}
		if err := sup.CreateInstance(req.Name, req.Content, req.Force); err != nil {
			writeErr(w, http.StatusBadRequest, err)
			return
		}
		writeJSON(w, okResp())
	})
	mux.HandleFunc("PUT /instances/{name}", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Content string `json:"content"`
			Force   bool   `json:"force"`
		}
		if !readJSON(w, r, &req) {
			return
		}
		if err := sup.UpdateInstance(r.PathValue("name"), req.Content, req.Force); err != nil {
			writeErr(w, http.StatusBadRequest, err)
			return
		}
		writeJSON(w, okResp())
	})
	mux.HandleFunc("DELETE /instances/{name}", func(w http.ResponseWriter, r *http.Request) {
		err := sup.DeleteInstance(r.PathValue("name"), r.URL.Query().Get("force") == "1")
		if err != nil {
			writeErr(w, http.StatusBadRequest, err)
			return
		}
		writeJSON(w, okResp())
	})
	mux.HandleFunc("POST /instances/{name}/up", actionHandler(sup.Start))
	mux.HandleFunc("POST /instances/{name}/down", actionHandler(sup.Stop))
	mux.HandleFunc("POST /instances/{name}/restart", actionHandler(sup.Restart))
	mux.HandleFunc("PATCH /instances/{name}", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Enabled *bool `json:"enabled"`
		}
		if !readJSON(w, r, &req) || req.Enabled == nil {
			writeErr(w, http.StatusBadRequest, fmt.Errorf("missing enabled"))
			return
		}
		if err := sup.SetEnabled(r.PathValue("name"), *req.Enabled); err != nil {
			writeErr(w, http.StatusBadRequest, err)
			return
		}
		writeJSON(w, okResp())
	})
	mux.HandleFunc("GET /logs", func(w http.ResponseWriter, r *http.Request) {
		handleLogs(w, r, ev)
	})
	mux.HandleFunc("GET /version", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]string{"version": version})
	})

	srv.http = &http.Server{Handler: mux}
	go srv.http.Serve(ln)
	return srv, nil
}

// version is set from daemon via SetVersion at startup.
var version = "dev"

// SetVersion records the binary version for /state.
func SetVersion(v string) { version = v }

// Close shuts the server and the listener down.
func (s *Server) Close() {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	s.http.Shutdown(ctx)
	s.ln.Close()
}

func actionHandler(fn func(string) error) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if err := fn(r.PathValue("name")); err != nil {
			writeErr(w, http.StatusBadRequest, err)
			return
		}
		writeJSON(w, okResp())
	}
}

// handleLogs serves filtered history, or with ?follow=1 a stream of NEW
// events as newline-delimited JSON until the client disconnects. Follow does
// not replay history — clients combine a history query with a follow stream.
func handleLogs(w http.ResponseWriter, r *http.Request, ev Logger) {
	q := r.URL.Query()
	filter := logs.Filter{
		Instance: q.Get("instance"),
		Text:     q.Get("q"),
	}
	if lv, err := logs.ParseLevel(q.Get("level")); err == nil {
		filter.MinLevel = lv
	}
	if n, err := strconv.Atoi(q.Get("limit")); err == nil {
		filter.Limit = n
	}
	if t, err := time.Parse(time.RFC3339, q.Get("since")); err == nil {
		filter.Since = t
	}
	if t, err := time.Parse(time.RFC3339, q.Get("until")); err == nil {
		filter.Until = t
	}

	fl, canFlush := w.(http.Flusher)
	w.Header().Set("Content-Type", "application/x-ndjson")
	enc := json.NewEncoder(w)
	if q.Get("follow") != "1" {
		hist := ev.QueryFile(filter)
		if hist == nil {
			hist = ev.Query(filter)
		}
		for _, e := range hist {
			enc.Encode(e)
		}
		if canFlush {
			fl.Flush()
		}
		return
	}
	ch, cancel := ev.Subscribe()
	defer cancel()
	for {
		select {
		case <-r.Context().Done():
			return
		case e, open := <-ch:
			if !open {
				return
			}
			if filter.Match(e) {
				if enc.Encode(e) != nil {
					return
				}
				if canFlush {
					fl.Flush()
				}
			}
		}
	}
}

// ok is the standard success body.
type ok struct{ OK bool }

func okResp() ok { return ok{OK: true} }

// ---- helpers ----

func adminGid() int {
	if g, err := user.LookupGroup("admin"); err == nil {
		if n, err := strconv.Atoi(g.Gid); err == nil {
			return n
		}
	}
	return 0 // fall back to root-only socket
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, code int, err error) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
}

func readJSON(w http.ResponseWriter, r *http.Request, v any) bool {
	if err := json.NewDecoder(r.Body).Decode(v); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return false
	}
	return true
}
