package api

import (
	"context"
	"testing"
	"time"

	"github.com/dncore/wg-service/internal/logs"
	"github.com/dncore/wg-service/internal/uapi"
	"github.com/dncore/wg-service/internal/wire"
	"path/filepath"
)

// fakeSup implements Supervisor for API tests.
type fakeSup struct {
	views   []wire.InstanceView
	confs   map[string]string
	enabled map[string]bool
	created map[string]string
	deleted map[string]bool
	lastAct string
	failOn  map[string]error
}

func (f *fakeSup) Views() []wire.InstanceView { return f.views }
func (f *fakeSup) LiveStatus(name string) (*uapi.DeviceStatus, time.Time, error) {
	for _, v := range f.views {
		if v.Name == name && v.Running {
			return &uapi.DeviceStatus{ListenPort: v.ListenPort, Peers: []uapi.PeerStatus{
				{Endpoint: "203.0.113.7:51820", RxBytes: 10, TxBytes: 20},
			}}, time.Now(), nil
		}
	}
	return nil, time.Time{}, &notRunning{name}
}

type notRunning struct{ name string }

func (e *notRunning) Error() string { return "instance not running: " + e.name }

func (f *fakeSup) Conf(name string) (string, error) { return f.confs[name], nil }
func (f *fakeSup) Start(name string) error          { f.lastAct = "up:" + name; return nil }
func (f *fakeSup) Stop(name string) error           { f.lastAct = "down:" + name; return nil }
func (f *fakeSup) Restart(name string) error        { f.lastAct = "restart:" + name; return nil }
func (f *fakeSup) SetEnabled(name string, en bool) error {
	f.enabled[name] = en
	return nil
}
func (f *fakeSup) CreateInstance(name, content string, force bool) error {
	if err := f.failOn["create"]; err != nil {
		return err
	}
	f.created[name] = content
	return nil
}
func (f *fakeSup) UpdateInstance(name, content string, force bool) error {
	f.confs[name] = content
	return nil
}
func (f *fakeSup) DeleteInstance(name string, force bool) error {
	f.deleted[name] = true
	return nil
}

func newTestServer(t *testing.T) (*Server, *fakeSup, *logs.Store, *Client) {
	t.Helper()
	fs := &fakeSup{
		views: []wire.InstanceView{
			{Name: "wg0", Enabled: true, Running: true, Tun: "utun3", Pid: 4242, ListenPort: 51820, PeerCount: 2, OnlinePeers: 1},
			{Name: "wg1", Enabled: false, ListenPort: 51821, PeerCount: 1},
		},
		confs:   map[string]string{"wg0": "[Interface]\n"},
		enabled: map[string]bool{},
		created: map[string]string{},
		deleted: map[string]bool{},
		failOn:  map[string]error{"create": nil},
	}
	ev, err := logs.New(filepath.Join(t.TempDir(), "events.jsonl"), 100, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	sock := filepath.Join(dir, "wgs.sock")
	srv, err := Serve(fs, ev, sock)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(srv.Close)
	return srv, fs, ev, Connect(sock)
}

func TestStateAndInstances(t *testing.T) {
	_, _, _, c := newTestServer(t)
	ctx := context.Background()
	st, err := c.State(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if st.Socket == "" || st.ConfDir == "" {
		t.Fatalf("state incomplete: %+v", st)
	}
	ins, err := c.Instances(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(ins) != 2 || ins[0].Name != "wg0" || !ins[0].Running || ins[1].Running {
		t.Fatalf("instances wrong: %+v", ins)
	}
}

func TestStatusDetail(t *testing.T) {
	_, _, _, c := newTestServer(t)
	s, err := c.Status(context.Background(), "wg0")
	if err != nil {
		t.Fatal(err)
	}
	if s.Status == nil || s.Status.ListenPort != 51820 {
		t.Fatalf("status wrong: %+v", s)
	}
	s, err = c.Status(context.Background(), "wg1")
	if err != nil {
		t.Fatal(err)
	}
	if s.Error == "" {
		t.Fatal("want error field for non-running instance")
	}
}

func TestLifecycleActions(t *testing.T) {
	_, fs, _, c := newTestServer(t)
	ctx := context.Background()
	for _, tc := range []struct {
		do   func() error
		want string
	}{
		{func() error { return c.Up(ctx, "wg0") }, "up:wg0"},
		{func() error { return c.Down(ctx, "wg0") }, "down:wg0"},
		{func() error { return c.Restart(ctx, "wg1") }, "restart:wg1"},
	} {
		if err := tc.do(); err != nil {
			t.Fatal(err)
		}
		if fs.lastAct != tc.want {
			t.Fatalf("action = %s want %s", fs.lastAct, tc.want)
		}
	}
	if err := c.SetEnabled(ctx, "wg1", true); err != nil {
		t.Fatal(err)
	}
	if !fs.enabled["wg1"] {
		t.Fatal("SetEnabled not applied")
	}
}

func TestCreateUpdateDelete(t *testing.T) {
	_, fs, _, c := newTestServer(t)
	ctx := context.Background()
	if err := c.Create(ctx, "wg2", "[Interface]\n", false); err != nil {
		t.Fatal(err)
	}
	if fs.created["wg2"] != "[Interface]\n" {
		t.Fatal("create content lost")
	}
	if err := c.Update(ctx, "wg2", "[Interface]\n#v2", false); err != nil {
		t.Fatal(err)
	}
	if fs.confs["wg2"] != "[Interface]\n#v2" {
		t.Fatal("update content lost")
	}
	if err := c.Delete(ctx, "wg2", true); err != nil {
		t.Fatal(err)
	}
	if !fs.deleted["wg2"] {
		t.Fatal("delete not applied")
	}
}

func TestLogsQueryAndFollow(t *testing.T) {
	_, _, ev, c := newTestServer(t)
	ev.Info("wg0", "alpha event")
	ev.Error("wg1", "beta event")
	hist, err := c.LogsQuery(context.Background(), logs.Filter{})
	if err != nil {
		t.Fatal(err)
	}
	if len(hist) != 2 {
		t.Fatalf("want 2 events, got %d", len(hist))
	}
	warn, err := c.LogsQuery(context.Background(), logs.Filter{MinLevel: logs.Warn})
	if err != nil {
		t.Fatal(err)
	}
	if len(warn) != 1 || warn[0].Msg != "beta event" {
		t.Fatalf("level filter broken: %+v", warn)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	got := make(chan logs.Event, 1)
	go c.LogsFollow(ctx, logs.Filter{}, func(e logs.Event) error {
		got <- e
		return nil
	})
	// give the follow request a moment to subscribe, then emit
	time.Sleep(200 * time.Millisecond)
	ev.Info("wg0", "live event")
	select {
	case e := <-got:
		if e.Msg != "live event" {
			t.Fatalf("live event wrong: %+v", e)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("follow did not deliver event")
	}
}

func TestErrorSurface(t *testing.T) {
	_, fs, _, c := newTestServer(t)
	fs.failOn["create"] = &conflictErr{}
	if err := c.Create(context.Background(), "wg9", "x", false); err == nil {
		t.Fatal("want conflict error surfaced to client")
	}
}

type conflictErr struct{}

func (*conflictErr) Error() string { return "ListenPort 51820 conflicts with instance \"wg0\"" }
