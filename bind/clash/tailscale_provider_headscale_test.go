//go:build with_gvisor && !no_tailscale

package clash

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	C "github.com/ProjectClash/Clash/constant"
	"github.com/ProjectClash/Clash/tunnel"
)

const tailscaleProviderFile = "ts-sub.yaml"

// tailscaleProviderPayload is a subscription with one tailscale node and,
// when extra is set, an unrelated socks node, so an update parses afresh.
func tailscaleProviderPayload(controlURL, authKey, hostname string, withNode, extra bool) string {
	payload := "proxies:\n"
	if withNode {
		payload += fmt.Sprintf("  - {name: ts, type: tailscale, hostname: %s, auth-key: %q, control-url: %q, state-dir: ts-provider, udp: true}\n",
			hostname, authKey, controlURL)
	}
	if extra || !withNode {
		payload += "  - {name: filler, type: socks5, server: 127.0.0.1, port: 1080}\n"
	}
	return payload
}

func tailscaleProviderDocument() string {
	return "mode: rule\nlog-level: warning\n" +
		"proxy-providers:\n  sub:\n    type: file\n    path: ./" + tailscaleProviderFile + "\n    health-check: {enable: false}\n" +
		"proxy-groups:\n  - {name: Mesh, type: select, use: [sub]}\n" +
		"rules:\n  - MATCH,DIRECT\n"
}

func writeTailscaleProvider(t *testing.T, payload string) {
	t.Helper()
	if err := os.WriteFile(C.Path.Resolve(tailscaleProviderFile), []byte(payload), 0o600); err != nil {
		t.Fatal(err)
	}
}

func providerTailscaleProxy(t *testing.T) C.Proxy {
	t.Helper()
	provider := tunnel.Providers()["sub"]
	if provider == nil {
		t.Fatal("the running configuration has no sub provider")
	}
	for _, proxy := range provider.Proxies() {
		if proxy.Name() == "ts" {
			return proxy
		}
	}
	t.Fatalf("the sub provider has no ts node (it has %d)", provider.Count())
	return nil
}

func dialProviderTailscale(t *testing.T, targetURL string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	if _, err := providerTailscaleProxy(t).URLTest(ctx, targetURL, nil); err != nil {
		t.Fatalf("tailscale dial through the provider: %s", externalInteropErrorCategory(err))
	}
}

func updateTailscaleProvider(t *testing.T) {
	t.Helper()
	provider := tunnel.Providers()["sub"]
	if provider == nil {
		t.Fatal("the running configuration has no sub provider")
	}
	if err := provider.Update(); err != nil {
		t.Fatalf("provider update: %v", err)
	}
}

// TestTailscaleProviderNodesKeepOneNode does for a node a subscription
// brings what TestTailscaleReloadKeepsOneNode does for one in proxies:
// reloads and provider updates that keep it unchanged keep the running
// node, and ones that change or drop it close the old node at once.
func TestTailscaleProviderNodesKeepOneNode(t *testing.T) {
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

	if err := Setup(testOptions(t)); err != nil {
		t.Fatal(err)
	}
	writeTailscaleProvider(t, tailscaleProviderPayload(controlURL, clientKey, "clash-sub", true, false))
	service, err := NewService(newRecordingPlatform())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = service.Close() })
	document := tailscaleProviderDocument()
	if err := service.Start(document); err != nil {
		t.Fatal(err)
	}
	dialProviderTailscale(t, targetURL)
	var listed tailscaleNodesResult
	if err := json.Unmarshal([]byte(TailscaleNodes()), &listed); err != nil {
		t.Fatal(err)
	}
	if len(listed.Nodes) != 1 || listed.Nodes[0] != (tailscaleNode{Name: "ts", Provider: "sub", Started: true}) {
		t.Errorf("TailscaleNodes = %+v; want the provider's running ts node", listed.Nodes)
	}
	first := sampleTailscaleReload()
	t.Logf("start: %s", first)

	const rounds = 3
	var last tailscaleReloadSample
	for i := 1; i <= rounds; i++ {
		if err := service.Reload(document); err != nil {
			t.Fatalf("reload %d: %v", i, err)
		}
		dialProviderTailscale(t, targetURL)
		last = sampleTailscaleReload()
		t.Logf("reload %d: %s", i, last)
	}
	if last.runningBeforeGC != 1 || last.sharedBeforeGC != 0 || last.starts != first.starts {
		t.Errorf("after %d reloads: %s; want one node, no shared state dir and no restart", rounds, last)
	}

	for i := 1; i <= rounds; i++ {
		// Alternate the filler node so every update parses a new list.
		writeTailscaleProvider(t, tailscaleProviderPayload(controlURL, clientKey, "clash-sub", true, i%2 == 1))
		updateTailscaleProvider(t)
		dialProviderTailscale(t, targetURL)
		last = sampleTailscaleReload()
		t.Logf("provider update %d: %s", i, last)
	}
	if last.runningBeforeGC != 1 || last.sharedBeforeGC != 0 || last.starts != first.starts {
		t.Errorf("after %d provider updates: %s; want one node, no shared state dir and no restart", rounds, last)
	}

	writeTailscaleProvider(t, tailscaleProviderPayload(controlURL, clientKey, "clash-sub-renamed", true, false))
	updateTailscaleProvider(t)
	dialProviderTailscale(t, targetURL)
	if got := sampleTailscaleReload(); got.runningBeforeGC != 1 || got.sharedBeforeGC != 0 || got.starts != last.starts+1 {
		t.Errorf("a provider update that changes the node: %s; want the old node closed and one new start", got)
	}

	writeTailscaleProvider(t, tailscaleProviderPayload(controlURL, clientKey, "", false, false))
	updateTailscaleProvider(t)
	if got := sampleTailscaleReload(); got.runningBeforeGC != 0 {
		t.Errorf("a provider update that drops the node: %s; want none running", got)
	}

	writeTailscaleProvider(t, tailscaleProviderPayload(controlURL, clientKey, "clash-sub", true, false))
	updateTailscaleProvider(t)
	dialProviderTailscale(t, targetURL)
	if err := service.Reload(helloYAML); err != nil {
		t.Fatal(err)
	}
	if got := sampleTailscaleReload(); got.runningBeforeGC != 0 {
		t.Errorf("a reload that drops the provider: %s; want none running", got)
	}
}

// TestTailscaleStatusResetRetiresTheNode checks reset as FlClash means it:
// the running node is closed and forgotten, its saved login is gone, and
// the next reload starts a fresh node that logs in again.
func TestTailscaleStatusResetRetiresTheNode(t *testing.T) {
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

	document := tailscaleReloadDocument(controlURL, clientKey, "clash-reset", false)
	service := startedServiceForReload(t, document)
	dialTailscaleThroughTunnel(t, targetURL)
	stateDir := C.Path.Resolve("ts-reload")
	if entries, _ := os.ReadDir(stateDir); len(entries) == 0 {
		t.Fatalf("the running node saved nothing in %s", stateDir)
	}
	before := sampleTailscaleReload()

	var reset tailscaleStatusResult
	if err := json.Unmarshal([]byte(TailscaleStatus(`{"name":"ts","reset":true}`)), &reset); err != nil {
		t.Fatal(err)
	}
	if reset.Started || reset.Peers == nil || len(reset.Peers) != 0 || reset.Error != "" {
		t.Errorf("reset returned %+v; want an empty, not started status", reset)
	}
	if got := sampleTailscaleReload(); got.runningBeforeGC != 0 {
		t.Errorf("after reset: %s; want the node closed", got)
	}
	if _, err := os.Stat(filepath.Join(stateDir, "tailscaled.state")); !os.IsNotExist(err) {
		t.Errorf("after reset the saved login is still in %s (%v)", stateDir, err)
	}

	if err := service.Reload(document); err != nil {
		t.Fatal(err)
	}
	dialTailscaleThroughTunnel(t, targetURL)
	if got := sampleTailscaleReload(); got.runningBeforeGC != 1 || got.starts != before.starts+1 {
		t.Errorf("the reload after reset: %s; want one fresh node", got)
	}
}
