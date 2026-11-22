//go:build linux

package daemon

import (
	"context"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"golang.zx2c4.com/wireguard/wgctrl"
	"golang.zx2c4.com/wireguard/wgctrl/wgtypes"

	"github.com/dncore/wgtun/internal/paths"
	"github.com/dncore/wgtun/internal/uapi"
	"github.com/dncore/wgtun/internal/wgconf"
)

// kernelClient caches the netlink client used for every kernel device.
var (
	wgOnce   sync.Once
	wgClient *wgctrl.Client
	wgErr    error
)

func kernelClient() (*wgctrl.Client, error) {
	wgOnce.Do(func() {
		wgClient, wgErr = wgctrl.New()
	})
	return wgClient, wgErr
}

// startKernelLocked creates the kernel WireGuard interface. The name is
// deterministic (linuxIfName), so a daemon restart and a cold boot both arrive
// at the same name.
func (s *Supervisor) startKernelLocked(i *inst) error {
	ifname := linuxIfName(i.name)
	if _, err := net.InterfaceByName(ifname); err == nil {
		// A device from a previous life that adoption could not verify (key
		// changed, wedged) or a leftover. The wgtun- prefix marks it as ours,
		// so replace it.
		s.ev.Warn(i.name, "interface %s already exists; replacing it", ifname)
		if out, err := exec.Command(ipPath(), "link", "del", ifname).CombinedOutput(); err != nil {
			return fmt.Errorf("remove stale interface %s: %v: %s", ifname, err, strings.TrimSpace(string(out)))
		}
	}
	if out, err := exec.Command(ipPath(), "link", "add", ifname, "type", "wireguard").CombinedOutput(); err != nil {
		return fmt.Errorf("create interface %s: %v: %s", ifname, err, strings.TrimSpace(string(out)))
	}
	i.tun = ifname
	i.pid = 0
	if err := os.WriteFile(filepath.Join(i.runtimeDir(), "tun.name"), []byte(ifname), 0o644); err != nil {
		s.ev.Warn(i.name, "write tun name: %v", err)
	}
	return nil
}

// pushConfKernelLocked applies the full configuration through netlink and
// returns the endpoints it pushed (for route pinning).
func (s *Supervisor) pushConfKernelLocked(i *inst) ([]string, error) {
	c, err := kernelClient()
	if err != nil {
		return nil, err
	}
	priv, err := wgtypes.ParseKey(i.conf.Interface.PrivateKey)
	if err != nil {
		return nil, fmt.Errorf("private key: %v", err)
	}
	cfg := wgtypes.Config{PrivateKey: &priv, ReplacePeers: true}
	if p := i.conf.Interface.ListenPort; p != 0 {
		cfg.ListenPort = &p
	}
	var endpoints []string
	for _, p := range i.conf.Peers {
		var endpoint *net.UDPAddr
		if p.Endpoint != "" {
			ctx, cancel := context.WithTimeout(context.Background(), endpointLookupTimeout)
			cands, rres, rerr := s.resolveEndpointAll(ctx, p.Endpoint)
			cancel()
			d := i.diagFor(endpointJob{peerKey: p.PublicKey, endpoint: p.Endpoint})
			d.CheckedAt = time.Now()
			if rerr != nil {
				d.Decision = "unresolved at start"
				d.Error = rerr.Error()
				s.ev.Warn(i.name, "endpoint %s unresolved (will retry): %v", p.Endpoint, rerr)
			} else {
				endpoint, err = net.ResolveUDPAddr("udp", cands[0])
				if err != nil {
					return nil, fmt.Errorf("endpoint %s: %v", cands[0], err)
				}
				endpoints = append(endpoints, cands[0])
				d.Resolver = rres.Server
				d.TTLSec = int64(rres.TTL.Seconds())
				d.Candidates = cands
				d.Live = cands[0]
				d.Decision = "pushed at start"
				d.FakeIP = fakeIPRange(rres)
				if d.FakeIP != "" && isHostnameEndpoint(p.Endpoint) {
					s.warnFakeIP(i, p.Endpoint, d.FakeIP)
				}
			}
		}
		pc, err := peerConfigFromConf(p, endpoint)
		if err != nil {
			return nil, err
		}
		cfg.Peers = append(cfg.Peers, pc)
	}
	if err := c.ConfigureDevice(i.tun, cfg); err != nil {
		return nil, err
	}
	return endpoints, nil
}

// kernelStatus reads live device state over netlink and projects it into the
// same shape the userspace UAPI parser produces.
func kernelStatus(ifname string) (*uapi.DeviceStatus, error) {
	c, err := kernelClient()
	if err != nil {
		return nil, err
	}
	dev, err := c.Device(ifname)
	if err != nil {
		return nil, err
	}
	st := &uapi.DeviceStatus{
		PublicKey:  dev.PublicKey.String(),
		ListenPort: dev.ListenPort,
	}
	for _, p := range dev.Peers {
		ps := uapi.PeerStatus{
			PublicKey:           p.PublicKey.String(),
			PersistentKeepalive: int(p.PersistentKeepaliveInterval.Seconds()),
			LastHandshake:       p.LastHandshakeTime,
			RxBytes:             uint64(p.ReceiveBytes),
			TxBytes:             uint64(p.TransmitBytes),
			HasPSK:              p.PresharedKey != (wgtypes.Key{}),
			ProtocolVersion:     p.ProtocolVersion,
		}
		if p.Endpoint != nil {
			ps.Endpoint = p.Endpoint.String()
		}
		for _, n := range p.AllowedIPs {
			ps.AllowedIPs = append(ps.AllowedIPs, n.String())
		}
		st.Peers = append(st.Peers, ps)
	}
	return st, nil
}

// kernelSetEndpoint retargets one peer without touching its session keys.
func kernelSetEndpoint(ifname, publicKey, endpoint string) error {
	c, err := kernelClient()
	if err != nil {
		return err
	}
	key, err := wgtypes.ParseKey(publicKey)
	if err != nil {
		return fmt.Errorf("peer public key: %v", err)
	}
	addr, err := net.ResolveUDPAddr("udp", endpoint)
	if err != nil {
		return fmt.Errorf("endpoint %s: %v", endpoint, err)
	}
	return c.ConfigureDevice(ifname, wgtypes.Config{Peers: []wgtypes.PeerConfig{{
		PublicKey:  key,
		UpdateOnly: true,
		Endpoint:   addr,
	}}})
}

// kernelUpsertPeer adds or updates one peer, allowed IPs included.
func kernelUpsertPeer(ifname string, p uapi.SetPeer) error {
	c, err := kernelClient()
	if err != nil {
		return err
	}
	key, err := wgtypes.ParseKey(p.PublicKey)
	if err != nil {
		return fmt.Errorf("peer public key: %v", err)
	}
	pc := wgtypes.PeerConfig{PublicKey: key, ReplaceAllowedIPs: true}
	if p.PresharedKey != "" {
		k, err := wgtypes.ParseKey(p.PresharedKey)
		if err != nil {
			return fmt.Errorf("preshared key: %v", err)
		}
		pc.PresharedKey = &k
	}
	if p.PersistentKeepalive != 0 {
		d := time.Duration(p.PersistentKeepalive) * time.Second
		pc.PersistentKeepaliveInterval = &d
	}
	if p.Endpoint != "" {
		addr, err := net.ResolveUDPAddr("udp", p.Endpoint)
		if err != nil {
			return fmt.Errorf("endpoint %s: %v", p.Endpoint, err)
		}
		pc.Endpoint = addr
	}
	for _, cidr := range p.AllowedIPs {
		_, n, err := net.ParseCIDR(cidr)
		if err != nil {
			return fmt.Errorf("allowed ip %s: %v", cidr, err)
		}
		pc.AllowedIPs = append(pc.AllowedIPs, *n)
	}
	return c.ConfigureDevice(ifname, wgtypes.Config{Peers: []wgtypes.PeerConfig{pc}})
}

// teardownKernelLocked removes the interface (and with it its routes).
func (s *Supervisor) teardownKernelLocked(i *inst) {
	if i.tun == "" {
		return
	}
	if out, err := exec.Command(ipPath(), "link", "del", i.tun).CombinedOutput(); err != nil {
		// Already gone is the common case after a reboot or a failed start.
		msg := string(out)
		if !strings.Contains(msg, "Cannot find device") && !strings.Contains(msg, "No such device") {
			s.ev.Warn(i.name, "remove interface %s: %v: %s", i.tun, err, strings.TrimSpace(msg))
		}
	}
}

// adoptKernelLocked re-attaches to a kernel interface that outlived its
// daemon. The device's public key must match the config, otherwise the
// interface belongs to a different config revision.
func (s *Supervisor) adoptKernelLocked(i *inst) bool {
	data, err := os.ReadFile(filepath.Join(i.runtimeDir(), "tun.name"))
	if err != nil {
		os.RemoveAll(i.runtimeDir())
		return false
	}
	ifname := strings.TrimSpace(string(data))
	if ifname == "" {
		os.RemoveAll(i.runtimeDir())
		return false
	}
	if _, err := net.InterfaceByName(ifname); err != nil {
		reapUserspaceLeftover(i, ifname)
		os.RemoveAll(i.runtimeDir())
		return false
	}
	st, err := kernelStatus(ifname)
	if err != nil {
		s.ev.Warn(i.name, "kernel interface %s not controllable, leaving it for restart: %v", ifname, err)
		reapUserspaceLeftover(i, ifname)
		os.RemoveAll(i.runtimeDir())
		return false
	}
	if want := i.devicePublicKey(); want != "" && st.PublicKey != "" && st.PublicKey != want {
		s.ev.Warn(i.name, "kernel interface %s has a different key; it will be recreated", ifname)
		os.RemoveAll(i.runtimeDir())
		return false
	}
	i.tun = ifname
	i.pid = 0
	i.active = true
	i.adopted = true
	i.startedAt = time.Now()
	for _, p := range st.Peers {
		if p.Endpoint != "" {
			s.pinEndpointFor(i, p.Endpoint)
		}
	}
	s.ev.Info(i.name, "adopted kernel interface %s", ifname)
	return true
}

// reapUserspaceLeftover kills a wireguard-go process recorded by an earlier
// userspace backend run when the daemon is now in kernel mode. Without this a
// backend switch would leave an orphaned process and UAPI socket behind.
func reapUserspaceLeftover(i *inst, ifname string) {
	pid := readPidFile(filepath.Join(i.runtimeDir(), "pid"))
	if pid > 0 && isWireGuardGo(pid) {
		syscall.Kill(pid, syscall.SIGTERM)
	}
	os.Remove(filepath.Join(paths.WireGuardSockDir, ifname+".sock"))
}

// peerConfigFromConf converts one parsed peer to wgtypes. endpoint is already
// resolved; nil means "no endpoint was configured or DNS failed".
func peerConfigFromConf(p wgconf.Peer, endpoint *net.UDPAddr) (wgtypes.PeerConfig, error) {
	key, err := wgtypes.ParseKey(p.PublicKey)
	if err != nil {
		return wgtypes.PeerConfig{}, fmt.Errorf("peer public key: %v", err)
	}
	pc := wgtypes.PeerConfig{PublicKey: key, ReplaceAllowedIPs: true, Endpoint: endpoint}
	if p.PresharedKey != "" {
		k, err := wgtypes.ParseKey(p.PresharedKey)
		if err != nil {
			return wgtypes.PeerConfig{}, fmt.Errorf("preshared key: %v", err)
		}
		pc.PresharedKey = &k
	}
	if p.PersistentKeepalive != 0 {
		d := time.Duration(p.PersistentKeepalive) * time.Second
		pc.PersistentKeepaliveInterval = &d
	}
	for _, ap := range p.AllowedIPs {
		pc.AllowedIPs = append(pc.AllowedIPs, net.IPNet{
			IP:   net.IP(ap.Addr().AsSlice()),
			Mask: net.CIDRMask(ap.Bits(), ap.Addr().BitLen()),
		})
	}
	return pc, nil
}
