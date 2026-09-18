package main

import (
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// The network probe of `dev trial --network`.
//
// It is the probe that found two holes in the macOS sandbox on 2026-09-17: a
// `hosts` tier whose profile never loaded, and a `localhost` tier that allowed
// every outbound TCP connection. Both had passed the enforcement suite, which
// probes an unroutable address that fails with or without a sandbox.
//
// So this one judges by what ARRIVES, and needs no internet. The CLI listens
// on four loopback ports and the plugin is told to:
//
//	declared    dial it through the proxy       — a connection MUST arrive
//	undeclared  dial it through the proxy       — none may (the allowlist)
//	direct      dial it with a raw socket       — none may (the sandbox)
//	done        dial it through the proxy, last — the "I have finished" signal
//
// `direct` IS on the plugin's allowlist. That is the point: the proxy would
// let it through, so a connection there can only mean the plugin reached the
// network without the proxy. The verdict is a connection count, so it does not
// depend on how an SDK speaks HTTP; the plugin uses https, which every SDK
// tunnels with CONNECT, and the failed handshake against a bare TCP listener
// is expected.
// nonLoopbackIPv4 is the address the `direct` listener binds: an address
// OUTSIDE loopback, so refusing it means the same thing on every OS. On
// Windows a `hosts`-tier plugin holds the AppContainer loopback exemption,
// which is all-or-nothing — a loopback `direct` target was reachable there
// and the check could not tell the sandbox's documented limit from a real
// leak (2026-09-18). Linux's empty netns and macOS's Seatbelt refuse both;
// only a non-loopback target lets Windows refuse too (WSAEACCES). Falls back
// to loopback, and says so, on a machine with no other interface.
func nonLoopbackIPv4() string {
	ifaces, err := net.Interfaces()
	if err != nil {
		return "127.0.0.1"
	}
	for _, ifc := range ifaces {
		if ifc.Flags&net.FlagUp == 0 || ifc.Flags&net.FlagLoopback != 0 {
			continue
		}
		addrs, err := ifc.Addrs()
		if err != nil {
			continue
		}
		for _, a := range addrs {
			if ipn, ok := a.(*net.IPNet); ok {
				if v4 := ipn.IP.To4(); v4 != nil && !v4.IsLoopback() && !v4.IsLinkLocalUnicast() {
					return v4.String()
				}
			}
		}
	}
	return "127.0.0.1"
}

type networkProbe struct {
	// directHost is where the `direct` listener lives (nonLoopbackIPv4).
	directHost string
	listeners  map[string]net.Listener
	hits       map[string]*atomic.Int64
	wg         sync.WaitGroup
}

var probeRoles = []string{"declared", "undeclared", "direct", "done"}

func startNetworkProbe() (*networkProbe, error) {
	p := &networkProbe{listeners: map[string]net.Listener{}, hits: map[string]*atomic.Int64{}, directHost: nonLoopbackIPv4()}
	for _, role := range probeRoles {
		host := "127.0.0.1"
		if role == "direct" {
			host = p.directHost
		}
		l, err := net.Listen("tcp", net.JoinHostPort(host, "0"))
		if err != nil {
			p.close()
			return nil, err
		}
		p.listeners[role] = l
		counter := &atomic.Int64{}
		p.hits[role] = counter
		p.wg.Add(1)
		go func() {
			defer p.wg.Done()
			for {
				c, err := l.Accept()
				if err != nil {
					return
				}
				counter.Add(1)
				c.Close()
			}
		}()
	}
	return p, nil
}

// host is where a role's listener lives: loopback, except `direct`.
func (p *networkProbe) host(role string) string {
	if role == "direct" {
		return p.directHost
	}
	return "127.0.0.1"
}

func (p *networkProbe) port(role string) int {
	return p.listeners[role].Addr().(*net.TCPAddr).Port
}

func (p *networkProbe) close() {
	for _, l := range p.listeners {
		l.Close()
	}
	p.wg.Wait()
}

// reset zeroes the counts. The scaffold's own `dev test` runs the plugin in the
// test harness, which does NOT sandbox it and sets no proxy — so the probe
// fires there too and dials all four ports directly. Those connections say
// nothing about the app; only what arrives after the plugin is installed does.
func (p *networkProbe) reset() {
	for _, c := range p.hits {
		c.Store(0)
	}
}

// declaredHosts is the manifest's allowlist: everything except `undeclared`.
// Loopback is granted per port, which is what makes four ports four policies.
func (p *networkProbe) declaredHosts() []any {
	var hosts []any
	for _, role := range []string{"declared", "direct", "done"} {
		hosts = append(hosts, net.JoinHostPort(p.host(role), strconv.Itoa(p.port(role))))
	}
	return hosts
}

// waitDone blocks until the plugin's last dial arrives.
func (p *networkProbe) waitDone(timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if p.hits["done"].Load() > 0 {
			// The other dials were issued first; give a straggler a moment.
			time.Sleep(300 * time.Millisecond)
			return true
		}
		time.Sleep(100 * time.Millisecond)
	}
	return false
}

// writeProbeSource adds the probe to the scaffold as its OWN file plus one
// call before the plugin runs, so the scaffold's source stays what `dev init`
// wrote.
func writeProbeSource(dir, tmpl string, p *networkProbe) error {
	ports := map[string]int{}
	for _, role := range probeRoles {
		ports[role] = p.port(role)
	}
	switch tmpl {
	case "py":
		src := fmt.Sprintf(`# Written by branchkit-cli dev trial --network.
import socket
import urllib.request


def install(plugin):
    @plugin.on_ready
    def _probe():
        def through_proxy(port):
            try:
                urllib.request.urlopen("https://127.0.0.1:%%d/" %% port, timeout=5).read()
            except Exception:
                pass

        through_proxy(%d)
        through_proxy(%d)
        try:
            socket.create_connection(("%s", %d), timeout=3).close()
        except Exception:
            pass
        through_proxy(%d)
`, ports["declared"], ports["undeclared"], p.directHost, ports["direct"], ports["done"])
		if err := os.WriteFile(filepath.Join(dir, "trial_probe.py"), []byte(src), 0o644); err != nil {
			return err
		}
		return injectBefore(filepath.Join(dir, "main.py"), "asyncio.run(plugin.run())",
			"import trial_probe\ntrial_probe.install(plugin)\n\n")

	case "ts":
		src := fmt.Sprintf(`// Written by branchkit-cli dev trial --network.
import { connect } from "node:net";
import type { Plugin } from "@branchkitdev/plugin-sdk-ts";

const throughProxy = async (port: number) => {
  try {
    await fetch("https://127.0.0.1:" + port + "/");
  } catch {}
};

const direct = (host: string, port: number) =>
  new Promise<void>((resolve) => {
    const s = connect({ host, port });
    const end = () => { s.destroy(); resolve(); };
    s.once("connect", end);
    s.once("error", end);
    setTimeout(end, 3000);
  });

export function installTrialProbe(plugin: Plugin): void {
  plugin.onReady(async () => {
    await throughProxy(%d);
    await throughProxy(%d);
    await direct("%s", %d);
    await throughProxy(%d);
  });
}
`, ports["declared"], ports["undeclared"], p.directHost, ports["direct"], ports["done"])
		if err := os.WriteFile(filepath.Join(dir, "src", "trial_probe.ts"), []byte(src), 0o644); err != nil {
			return err
		}
		if err := injectBefore(filepath.Join(dir, "src", "index.ts"), "await plugin.run();",
			"installTrialProbe(plugin);\n\n"); err != nil {
			return err
		}
		return injectBefore(filepath.Join(dir, "src", "index.ts"), "import { Plugin }",
			"import { installTrialProbe } from \"./trial_probe\";\n")

	case "go":
		src := fmt.Sprintf(`// Written by branchkit-cli dev trial --network.
package main

import (
	"fmt"
	"net"
	"net/http"
	"time"

	"github.com/branchkit/plugin-sdk-go"
)

func installTrialProbe(plugin *branchkit.Plugin) {
	throughProxy := func(port int) {
		client := &http.Client{Timeout: 5 * time.Second}
		if resp, err := client.Get(fmt.Sprintf("https://127.0.0.1:%%d/", port)); err == nil {
			resp.Body.Close()
		}
	}
	plugin.OnReady(func() {
		throughProxy(%d)
		throughProxy(%d)
		if c, err := net.DialTimeout("tcp", "%s:%d", 3*time.Second); err == nil {
			c.Close()
		}
		throughProxy(%d)
	})
}
`, ports["declared"], ports["undeclared"], p.directHost, ports["direct"], ports["done"])
		if err := os.WriteFile(filepath.Join(dir, "src", "trial_probe.go"), []byte(src), 0o644); err != nil {
			return err
		}
		return injectBefore(filepath.Join(dir, "src", "main.go"), "\tplugin.Run()", "\tinstallTrialProbe(plugin)\n\n")
	}
	return fmt.Errorf("no network probe for template %q", tmpl)
}

// injectBefore inserts text immediately before the first occurrence of anchor.
func injectBefore(path, anchor, text string) error {
	raw, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	body := string(raw)
	i := strings.Index(body, anchor)
	if i < 0 {
		return fmt.Errorf("%s: could not find %q to attach the probe to — has the template changed?", filepath.Base(path), anchor)
	}
	return os.WriteFile(path, []byte(body[:i]+text+body[i:]), 0o644)
}

// verdict turns the connection counts into the three findings.
func (p *networkProbe) verdict(t *trialRun) {
	n := func(role string) int64 { return p.hits[role].Load() }

	var err error
	if n("declared") == 0 {
		err = fmt.Errorf("no connection arrived — the plugin could not reach a host it DECLARED through its proxy")
	}
	t.record("network: a declared host is reachable through the proxy", err, "")

	err = nil
	if n("undeclared") > 0 {
		err = fmt.Errorf("%d connection(s) arrived at a host the plugin never declared — the allowlist is not being enforced", n("undeclared"))
	}
	t.record("network: an undeclared host is refused", err, "")

	err = nil
	if n("direct") > 0 {
		err = fmt.Errorf("%d connection(s) arrived from a RAW socket — the plugin reached the network without its proxy, so the sandbox is not confining it", n("direct"))
	}
	t.record("network: a direct socket is refused by the sandbox", err, "")
}

// checkRecord asks the app what it RECORDED and holds that against what
// arrived on the wire. The network view is only worth reading if it is this
// accurate: the declared dial listed as allowed, the undeclared one listed as
// refused, and the direct socket not listed at all — it never reached the
// proxy, the sandbox stopped it first.
func (p *networkProbe) checkRecord(t *trialRun, token, id string) {
	raw, status, err := devHTTP("GET", "/v1/plugins/"+id+"/network", token, nil)
	if err == nil && status != 200 {
		err = fmt.Errorf("HTTP %d", status)
	}
	var report struct {
		Tier    string `json:"tier"`
		Targets []struct {
			Port    int   `json:"port"`
			Allowed int64 `json:"allowed"`
			Denied  int64 `json:"denied"`
		} `json:"targets"`
	}
	if err == nil {
		err = json.Unmarshal(raw, &report)
	}
	if err != nil {
		t.record("network: the app's record matches the wire", err, "")
		return
	}
	byPort := map[int][2]int64{}
	for _, tg := range report.Targets {
		byPort[tg.Port] = [2]int64{tg.Allowed, tg.Denied}
	}
	var problems []string
	if byPort[p.port("declared")][0] == 0 {
		problems = append(problems, "the declared host is not recorded as allowed")
	}
	if u := byPort[p.port("undeclared")]; u[1] == 0 || u[0] > 0 {
		problems = append(problems, "the undeclared host is not recorded as refused")
	}
	if _, listed := byPort[p.port("direct")]; listed {
		problems = append(problems, "the direct-socket port is in the record, so something dialed it through the proxy")
	}
	if len(problems) > 0 {
		err = fmt.Errorf("%s", strings.Join(problems, "; "))
	}
	t.record("network: the app's record matches the wire", err, fmt.Sprintf("tier %s, %d host(s) recorded", report.Tier, len(report.Targets)))
}
