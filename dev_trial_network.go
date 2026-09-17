package main

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
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
type networkProbe struct {
	listeners map[string]net.Listener
	hits      map[string]*atomic.Int64
	wg        sync.WaitGroup
}

var probeRoles = []string{"declared", "undeclared", "direct", "done"}

func startNetworkProbe() (*networkProbe, error) {
	p := &networkProbe{listeners: map[string]net.Listener{}, hits: map[string]*atomic.Int64{}}
	for _, role := range probeRoles {
		l, err := net.Listen("tcp", "127.0.0.1:0")
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
		hosts = append(hosts, fmt.Sprintf("127.0.0.1:%d", p.port(role)))
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
            socket.create_connection(("127.0.0.1", %d), timeout=3).close()
        except Exception:
            pass
        through_proxy(%d)
`, ports["declared"], ports["undeclared"], ports["direct"], ports["done"])
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

const direct = (port: number) =>
  new Promise<void>((resolve) => {
    const s = connect({ host: "127.0.0.1", port });
    const end = () => { s.destroy(); resolve(); };
    s.once("connect", end);
    s.once("error", end);
    setTimeout(end, 3000);
  });

export function installTrialProbe(plugin: Plugin): void {
  plugin.onReady(async () => {
    await throughProxy(%d);
    await throughProxy(%d);
    await direct(%d);
    await throughProxy(%d);
  });
}
`, ports["declared"], ports["undeclared"], ports["direct"], ports["done"])
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
		if c, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%%d", %d), 3*time.Second); err == nil {
			c.Close()
		}
		throughProxy(%d)
	})
}
`, ports["declared"], ports["undeclared"], ports["direct"], ports["done"])
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
