package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"sort"
	"sync"
	"time"

	"github.com/artyomsv/quil/internal/config"
	"github.com/artyomsv/quil/internal/ipc"
)

// The MCP bridge used to hold ONE daemon connection: the local socket. The
// TUI, meanwhile, drives every `[[destinations]]` host through its Router,
// and a project on such a host was invisible to every MCP tool.
//
// mcpRouter is the bridge's equivalent: one mcpBridge per configured host,
// dialled in the background at startup with the same ssh transport, version
// gate and hello the TUI's background dials use, and re-dialled lazily with
// a backoff when a tool call names a host that is down. Tools address a host
// explicitly (`host`), or through the id→host cache every list and create
// fills, or fall back to the local daemon.

// hostRedialBackoff is how long a failed dial holds before a tool call may
// try that host again. A tool that names a dead host every second must not
// spawn an ssh per second.
const hostRedialBackoff = 30 * time.Second

// hostDialFn dials one destination and returns a connected, hello'd client.
// A seam so the router's routing logic tests without ssh.
type hostDialFn func(cfg config.Config, d config.Destination) (*ipc.Client, error)

// dialMCPHost is the production dial: batch ssh (no prompts — the bridge has
// no terminal), the version gate, and the bridge-role hello.
func dialMCPHost(cfg config.Config, d config.Destination) (*ipc.Client, error) {
	ctx, cancel := context.WithTimeout(context.Background(), extraDialTimeout)
	defer cancel()
	sink := &sshStderrLogger{dest: d.Dest}
	client, link, err := dialRemoteTransportFn(ctx, cfg, d.Dest, true, sink)
	if err != nil {
		return nil, err
	}
	if client == nil {
		return nil, errors.New("ssh dial returned no connection")
	}
	if gateErr := gateExtraVersion(d, client, link); gateErr != nil {
		client.Close()
		return nil, classifyDialFailure(link, gateErr)
	}
	sendClientHello(client, helloRoleBridge)
	return client, nil
}

type hostConn struct {
	dest    config.Destination
	mu      sync.Mutex
	bridge  *mcpBridge
	client  *ipc.Client
	cancel  context.CancelFunc
	err     error
	lastTry time.Time
}

// hostStatus is the list_hosts view.
type hostStatus struct {
	Host      string `json:"host"`
	Label     string `json:"label,omitempty"`
	Connected bool   `json:"connected"`
	Error     string `json:"error,omitempty"`
}

type mcpRouter struct {
	local   *mcpBridge
	cfg     config.Config
	dial    hostDialFn
	backoff time.Duration

	mu     sync.Mutex
	hosts  map[string]*hostConn
	order  []string
	idHost map[string]string

	// selfPane is the pane this bridge runs inside — QUIL_PANE_ID, which the
	// daemon sets on every AI pane's child and the bridge inherits as that
	// child's child. Empty for a bridge spawned outside any pane.
	selfPane string
}

func newMCPRouter(local *mcpBridge, cfg config.Config, dial hostDialFn) *mcpRouter {
	r := &mcpRouter{
		local:    local,
		cfg:      cfg,
		dial:     dial,
		backoff:  hostRedialBackoff,
		hosts:    make(map[string]*hostConn),
		idHost:   make(map[string]string),
		selfPane: os.Getenv("QUIL_PANE_ID"),
	}
	seen := map[string]bool{}
	for _, d := range cfg.Destinations {
		if d.Dest == "" || seen[d.Dest] {
			continue
		}
		seen[d.Dest] = true
		r.hosts[d.Dest] = &hostConn{dest: d}
		r.order = append(r.order, d.Dest)
	}
	return r
}

// connectAll dials every configured host in the background. Best effort:
// the bridge serves the local daemon whether or not any remote answers.
func (r *mcpRouter) connectAll() {
	for _, dest := range r.order {
		h := r.hosts[dest]
		go func() {
			if err := r.connect(h, true); err != nil {
				log.Printf("mcp: host %s: %v", h.dest.Label(), err)
			}
		}()
	}
}

// connect ensures h has a live bridge, dialling if needed. `initial` skips
// the backoff so the startup sweep always tries once.
func (r *mcpRouter) connect(h *hostConn, initial bool) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.bridge != nil && !h.bridge.dead.Load() {
		return nil
	}
	if h.bridge != nil {
		// The read loop ended: the link dropped. Release the old client.
		h.cancel()
		h.client.Close()
		h.bridge, h.client, h.cancel = nil, nil, nil
		h.err = errors.New("connection lost")
		// A LOSS earns one immediate retry: the backoff exists for a host
		// that refused the last dial, and this host accepted it.
		h.lastTry = time.Time{}
	}
	if !initial && h.err != nil && time.Since(h.lastTry) < r.backoff {
		return fmt.Errorf("host %s unreachable: %v (retry in %s)", h.dest.Label(), h.err,
			(r.backoff - time.Since(h.lastTry)).Round(time.Second))
	}
	h.lastTry = time.Now()
	client, err := r.dial(r.cfg, h.dest)
	if err != nil {
		h.err = err
		return fmt.Errorf("host %s unreachable: %w", h.dest.Label(), err)
	}
	bridge := newMCPBridge(client)
	if err := bridge.declinePaneOutput(); err != nil {
		log.Printf("mcp: host %s: decline pane output: %v", h.dest.Label(), err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	go bridge.readLoop(ctx)
	h.bridge, h.client, h.cancel, h.err = bridge, client, cancel, nil
	return nil
}

// bridgeFor resolves which daemon a tool call is aimed at.
//
//  1. An explicit host wins. "local" and "" both mean the local daemon.
//  2. Else any id the call carries that the cache has filed under a host.
//  3. Else local.
//
// The cache is filled by every list and create, so an agent that discovered
// a pane through list_panes can act on it without repeating the host — and
// an id it never saw is assumed local, which is where every pre-existing
// caller's ids live.
func (r *mcpRouter) bridgeFor(host string, ids ...string) (*mcpBridge, string, error) {
	if host == "" {
		r.mu.Lock()
		for _, id := range ids {
			if id == "" {
				continue
			}
			if h, ok := r.idHost[id]; ok {
				host = h
				break
			}
		}
		r.mu.Unlock()
	}
	if host == "" || host == "local" {
		return r.local, "", nil
	}
	r.mu.Lock()
	h, ok := r.hosts[host]
	r.mu.Unlock()
	if !ok {
		return nil, "", fmt.Errorf("unknown host %q: not in [[destinations]] (see list_hosts)", host)
	}
	if err := r.connect(h, false); err != nil {
		return nil, "", err
	}
	h.mu.Lock()
	b := h.bridge
	h.mu.Unlock()
	return b, host, nil
}

// remember files ids under a host so later calls route without naming it.
// Local ids are filed too, so a stale remote entry for a reused id is
// overwritten rather than kept.
func (r *mcpRouter) remember(host string, ids ...string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, id := range ids {
		if id == "" {
			continue
		}
		if host == "" {
			delete(r.idHost, id)
		} else {
			r.idHost[id] = host
		}
	}
}

// connected returns the local bridge first, then every remote host that is
// currently connected, in config order. Hosts that are down are skipped —
// an aggregate list must not block on a dead ssh.
func (r *mcpRouter) connected() []hostBridge {
	out := []hostBridge{{host: "", bridge: r.local}}
	for _, dest := range r.order {
		h := r.hosts[dest]
		h.mu.Lock()
		if h.bridge != nil && !h.bridge.dead.Load() {
			out = append(out, hostBridge{host: dest, bridge: h.bridge})
		}
		h.mu.Unlock()
	}
	return out
}

type hostBridge struct {
	host   string
	bridge *mcpBridge
}

// targets is what an aggregating tool iterates: every connected host when no
// host is named, else that one host (dialling it if needed). An unknown or
// unreachable named host yields nothing — the tool's per-host request then
// reports the failure through bridgeFor's error on the next call, and an
// aggregate never blocks on a dead ssh.
func (r *mcpRouter) targets(host string) []hostBridge {
	if host == "" {
		return r.connected()
	}
	b, h, err := r.bridgeFor(host)
	if err != nil {
		return nil
	}
	return []hostBridge{{host: h, bridge: b}}
}

// watchTargets picks the hosts a watch spans: the named host; else the hosts
// the watched pane ids were discovered on; else every connected host.
func (r *mcpRouter) watchTargets(host string, paneIDs []string) []hostBridge {
	if host != "" {
		return r.targets(host)
	}
	seen := map[string]bool{}
	var out []hostBridge
	r.mu.Lock()
	hosts := make([]string, 0, len(paneIDs))
	for _, id := range paneIDs {
		h, ok := r.idHost[id]
		if !ok {
			h = ""
		}
		if !seen[h] {
			seen[h] = true
			hosts = append(hosts, h)
		}
	}
	r.mu.Unlock()
	if len(paneIDs) == 0 {
		return r.connected()
	}
	for _, h := range hosts {
		b, hh, err := r.bridgeFor(h)
		if err != nil {
			continue
		}
		out = append(out, hostBridge{host: hh, bridge: b})
	}
	return out
}

// statuses is the list_hosts answer: every configured host, connected or not.
func (r *mcpRouter) statuses() []hostStatus {
	out := make([]hostStatus, 0, len(r.order))
	for _, dest := range r.order {
		h := r.hosts[dest]
		h.mu.Lock()
		st := hostStatus{Host: dest, Label: h.dest.Name}
		st.Connected = h.bridge != nil && !h.bridge.dead.Load()
		if !st.Connected && h.err != nil {
			st.Error = h.err.Error()
		}
		h.mu.Unlock()
		out = append(out, st)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Connected && !out[j].Connected })
	return out
}
