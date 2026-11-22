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

	"github.com/dncore/wgtun/internal/logs"
	"github.com/dncore/wgtun/internal/paths"
	"github.com/dncore/wgtun/internal/uapi"
	"github.com/dncore/wgtun/internal/wire"
)

// Supervisor is the daemon surface the API needs.
type Supervisor interface {
	Views() []wire.InstanceView
	LiveStatus(name string) (*uapi.DeviceStatus, time.Time, error)
	DevicePublicKey(name string) string
	EndpointDiags(name string) []wire.EndpointDiag
	ResolverInfo() string
	LastReconcile() time.Time
	LastReconcileDuration() time.Duration
	LastBeat() time.Time
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
	// group-writable for the configured/service group so the unprivileged TUI
	// can connect. No suitable group means root-only (0600).
	if gid := socketGID(); gid > 0 {
		if err := os.Chown(sockPath, 0, gid); err == nil {
			os.Chmod(sockPath, 0o660)
		}
	}

	srv := &Server{ln: ln}
	started := time.Now()

	mux := http.NewServeMux()
	mux.HandleFunc("GET /state", func(w http.ResponseWriter, r *http.Request) {
		last := sup.LastReconcile()
		age := time.Since(last)
		interval := time.Duration(reconcileIntervalSec) * time.Second
		// Health tracks progress, not completion: a pass over many slow DNS
		// lookups is legitimate, while a loop that stopped beating is not.
		progressAge := time.Since(sup.LastBeat())
		staleAfter := 10 * interval
		if staleAfter < 2*time.Minute {
			staleAfter = 2 * time.Minute
		}
		writeJSON(w, wire.StateInfo{
			Version: version, Started: started, Socket: sockPath,
			ConfDir: paths.ConfDir, RunDir: paths.RunDir, LogDir: paths.LogDir,
			UpSec:                int64(time.Since(started).Seconds()),
			WireGuardGoPath:      wgGoPath,
			WireGuardGoVersion:   wgGoVersion,
			WireGuardGoOK:        wgGoOK,
			EndpointResolver:     sup.ResolverInfo(),
			Backend:              backendName,
			BackendDetail:        backendDetail,
			Pid:                  os.Getpid(),
			LastReconcile:        last,
			ReconcileAgeSec:      age.Seconds(),
			ReconcileLastSec:     sup.LastReconcileDuration().Seconds(),
			ReconcileIntervalSec: int64(interval.Seconds()),
			Healthy:              progressAge <= staleAfter,
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
		resp.DevicePublicKey = sup.DevicePublicKey(name)
		resp.Endpoints = sup.EndpointDiags(name)
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

// reconcileIntervalSec is the daemon's configured reconcile cadence, set
// before Serve starts. The TUI uses it to judge whether the loop is stale.
var reconcileIntervalSec int64

// SetReconcileInterval records the reconcile cadence for /state.
func SetReconcileInterval(d time.Duration) { reconcileIntervalSec = int64(d.Seconds()) }

// wireguard-go detection result, set from the daemon at startup.
var (
	wgGoPath    string
	wgGoVersion string
	wgGoOK      bool
)

// backend selection, set from the daemon at startup.
var (
	backendName   = "userspace"
	backendDetail string
)

// SetBackendInfo records the selected WireGuard backend for /state.
func SetBackendInfo(name, detail string) {
	backendName, backendDetail = name, detail
}

// SetWireGuardGoInfo records the detected userspace implementation for /state.
func SetWireGuardGoInfo(path string, ok bool, version string) {
	wgGoPath, wgGoOK, wgGoVersion = path, ok, version
}

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

// socketGID resolves the group that may connect to the control socket:
// WGTUN_SOCKET_GROUP (name or numeric), then the platform's conventional
// admin groups. 0 means root-only.
func socketGID() int {
	if spec := paths.SocketGroup(); spec != "" {
		if n, err := strconv.Atoi(spec); err == nil {
			return n
		}
		if g, err := user.LookupGroup(spec); err == nil {
			if n, err := strconv.Atoi(g.Gid); err == nil {
				return n
			}
		}
	}
	// A foreground `sudo wgtun daemon` carries the caller's gid; use it for
	// the debug run instead of falling back to wheel/sudo membership.
	if gid := os.Getenv("SUDO_GID"); gid != "" {
		if n, err := strconv.Atoi(gid); err == nil {
			return n
		}
	}
	for _, name := range defaultSocketGroups {
		if g, err := user.LookupGroup(name); err == nil {
			if n, err := strconv.Atoi(g.Gid); err == nil {
				return n
			}
		}
	}
	return 0
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
