//go:build with_gvisor && !no_tailscale

package clash

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"runtime"
	"runtime/debug"
	"strconv"
	"strings"
	"testing"
	"time"

	"os"

	"github.com/ProjectClash/Clash/adapter/outbound"
	"github.com/ProjectClash/Clash/tunnel"
)

// tailscaleReloadDocument is a profile whose only real outbound is one
// tailscale node, optionally behind a select group, with the given hostname.
func tailscaleReloadDocument(controlURL, authKey, hostname string, grouped bool) string {
	var b strings.Builder
	b.WriteString("mode: rule\nlog-level: warning\nproxies:\n")
	fmt.Fprintf(&b, "  - {name: ts, type: tailscale, hostname: %s, auth-key: %q, control-url: %q, state-dir: ts-reload, udp: true}\n",
		hostname, authKey, controlURL)
	if grouped {
		b.WriteString("proxy-groups:\n  - {name: Mesh, type: select, proxies: [ts]}\n")
	}
	b.WriteString("rules:\n  - MATCH,DIRECT\n")
	return b.String()
}

type tailscaleReloadSample struct {
	runningBeforeGC int
	sharedBeforeGC  int
	running         int
	shared          int
	starts          int
	heapMiB         float64
}

// sampleTailscaleReload reads the node counts first as the reload left them,
// then again after a forced GC, which runs the finalizers that close any
// replaced outbound nobody closed.
func sampleTailscaleReload() tailscaleReloadSample {
	before, sharedBefore := outbound.RunningTailscaleNodes(), len(outbound.SharedTailscaleStateDirs())
	runtime.GC()
	runtime.GC()
	debug.FreeOSMemory()
	var ms runtime.MemStats
	runtime.ReadMemStats(&ms)
	time.Sleep(200 * time.Millisecond) // finalizers run on their own goroutine
	return tailscaleReloadSample{
		runningBeforeGC: before,
		sharedBeforeGC:  sharedBefore,
		starts:          outbound.TailscaleNodeStarts(),
		running:         outbound.RunningTailscaleNodes(),
		shared:          len(outbound.SharedTailscaleStateDirs()),
		heapMiB:         float64(ms.HeapInuse) / (1 << 20),
	}
}

func (s tailscaleReloadSample) String() string {
	return fmt.Sprintf("running=%d shared-dirs=%d (before GC) running=%d shared-dirs=%d (after GC) starts=%d heap=%.1fMiB",
		s.runningBeforeGC, s.sharedBeforeGC, s.running, s.shared, s.starts, s.heapMiB)
}

func dialTailscaleThroughTunnel(t *testing.T, targetURL string) {
	t.Helper()
	dialThroughTunnelProxy(t, "ts", targetURL)
}

func dialThroughTunnelProxy(t *testing.T, name, targetURL string) {
	t.Helper()
	proxy := tunnel.Proxies()[name]
	if proxy == nil {
		t.Fatalf("the running configuration has no %s proxy", name)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	if _, err := proxy.URLTest(ctx, targetURL, nil); err != nil {
		t.Fatalf("tailscale dial after reload: %s", externalInteropErrorCategory(err))
	}
}

// TestTailscaleReloadKeepsOneNode reloads a profile with a tailscale node N
// times, dialing through it each time, and checks that only one tsnet node
// runs on the state dir afterwards. Before the reconcile, every reload left
// the replaced node running on the same state dir.
func TestTailscaleReloadKeepsOneNode(t *testing.T) {
	reference := os.Getenv(headscaleReferenceEnvironment)
	if reference == "" {
		t.Skip(headscaleReferenceEnvironment + " is not set")
	}
	verifyHeadscaleReference(t, reference)
	controlURL, configPath := startHeadscaleReference(t, reference)
	userID := createHeadscaleUser(t, reference, configPath)
	targetKey := createHeadscalePreauthKey(t, reference, configPath, userID)
	clientKey := createHeadscalePreauthKey(t, reference, configPath, userID)
	target, _, tcpPort, _, _, _ := startTailnetTarget(t, controlURL, targetKey)
	t.Cleanup(func() { _ = target.Close() })
	v4, _ := target.TailscaleIPs()
	targetURL := "http://" + net.JoinHostPort(v4.String(), strconv.Itoa(tcpPort)) + "/generate_204"

	const reloads = 5
	for _, grouped := range []bool{false, true} {
		t.Run(fmt.Sprintf("grouped=%v", grouped), func(t *testing.T) {
			document := tailscaleReloadDocument(controlURL, clientKey, "clash-reload", grouped)
			service := startedServiceForReload(t, document)
			dialTailscaleThroughTunnel(t, targetURL)
			first := sampleTailscaleReload()
			t.Logf("start: %s", first)
			var last tailscaleReloadSample
			for i := 1; i <= reloads; i++ {
				if err := service.Reload(document); err != nil {
					t.Fatalf("reload %d: %v", i, err)
				}
				dialTailscaleThroughTunnel(t, targetURL)
				if grouped {
					dialThroughTunnelProxy(t, "Mesh", targetURL)
				}
				last = sampleTailscaleReload()
				t.Logf("reload %d: %s", i, last)
			}
			if last.runningBeforeGC != 1 || last.sharedBeforeGC != 0 || last.running != 1 || last.shared != 0 {
				t.Errorf("after %d reloads: %s; want one node and no shared state dir, before and after GC", reloads, last)
			}
			if last.starts != first.starts {
				t.Errorf("%d reloads of an unchanged node restarted it %d times; want it kept", reloads, last.starts-first.starts)
			}

			checkTailscaleExports(t, v4.String())

			changed := tailscaleReloadDocument(controlURL, clientKey, "clash-reload-renamed", grouped)
			if err := service.Reload(changed); err != nil {
				t.Fatal(err)
			}
			dialTailscaleThroughTunnel(t, targetURL)
			if got := sampleTailscaleReload(); got.runningBeforeGC != 1 || got.sharedBeforeGC != 0 || got.running != 1 {
				t.Errorf("a reload that changes the node: %s; want one node and no shared state dir", got)
			}

			if err := service.Reload(helloYAML); err != nil {
				t.Fatal(err)
			}
			if got := sampleTailscaleReload(); got.runningBeforeGC != 0 || got.running != 0 {
				t.Errorf("a reload that drops the node: %s; want none running", got)
			}
			if err := service.Close(); err != nil {
				t.Fatal(err)
			}
			if got := outbound.RunningTailscaleNodes(); got != 0 {
				t.Errorf("closing the service left %d tsnet nodes running", got)
			}
		})
	}
}

// checkTailscaleExports calls the app-facing exports against the running
// node and checks the JSON they return.
func checkTailscaleExports(t *testing.T, peer string) {
	t.Helper()
	var status tailscaleStatusResult
	if err := json.Unmarshal([]byte(TailscaleStatus(`{"name":"ts","wait":true}`)), &status); err != nil {
		t.Fatal(err)
	}
	if !status.Started || status.BackendState != "Running" || status.Self == nil || len(status.Peers) == 0 || status.Error != "" {
		t.Errorf("TailscaleStatus = %+v; want a running node with a peer", status)
	}
	if status.DERPRoutes["clash-lab"] != "direct" {
		t.Errorf("TailscaleStatus derpRoutes = %v; want the self-hosted clash-lab region direct", status.DERPRoutes)
	}
	var pings []tailscalePingResult
	if err := json.Unmarshal([]byte(TailscalePing(`{"name":"ts","targets":["`+peer+`","not-an-ip"]}`)), &pings); err != nil {
		t.Fatal(err)
	}
	if len(pings) != 2 || pings[0].LatencyMs <= 0 || pings[0].Error != "" || pings[1].LatencyMs != -1 || pings[1].Error == "" {
		t.Errorf("TailscalePing = %+v; want the peer answered and the bad target refused", pings)
	}
	var routes tailscaleDERPRoutesResult
	if err := json.Unmarshal([]byte(TailscaleDERPRoutes("ts")), &routes); err != nil {
		t.Fatal(err)
	}
	if routes.DERPRoutes["clash-lab"] != "direct" || routes.Error != "" {
		t.Errorf("TailscaleDERPRoutes = %+v", routes)
	}
	var changed tailscaleActionResult
	if err := json.Unmarshal([]byte(TailscaleNetworkChanged()), &changed); err != nil {
		t.Fatal(err)
	}
	if changed.Nodes != 1 || changed.Error != "" {
		t.Errorf("TailscaleNetworkChanged = %+v; want one node told", changed)
	}
	var reconnect tailscaleActionResult
	if err := json.Unmarshal([]byte(TailscaleReconnect("ts")), &reconnect); err != nil {
		t.Fatal(err)
	}
	if reconnect.Error != "" {
		t.Errorf("TailscaleReconnect on a running node = %+v; want nothing to do", reconnect)
	}
	t.Logf("exports: state=%s peers=%d derp=%v ping=%dms via %q", status.BackendState, len(status.Peers), status.DERPRoutes, pings[0].LatencyMs, pings[0].Endpoint)
}
