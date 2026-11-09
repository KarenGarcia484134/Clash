//go:build with_gvisor && !no_tailscale

package clash

import (
	"sync"

	"github.com/ProjectClash/Clash/adapter"
	"github.com/ProjectClash/Clash/adapter/outbound"
	C "github.com/ProjectClash/Clash/constant"
	"github.com/ProjectClash/Clash/log"
)

// Every Start and Reload parses a fresh tailscale outbound, and nothing
// closed the one it replaced: only the finalizer on its wrapper did, at
// some later GC. Until then two tsnet nodes ran on the same state dir with
// the same machine key, and every reload restarted the node, dropping its
// connections and logging in again. reconcileTailscaleNodes runs right
// before a parsed configuration is applied: an unchanged node keeps the
// running one, by giving the new Proxy the old adapter before anything,
// groups included, can use it; a node that changed or is gone is closed
// now, before its replacement can start.
var (
	liveTailscaleMu sync.Mutex
	liveTailscale   = map[string]C.ProxyAdapter{}
)

func reconcileTailscaleNodes(proxies map[string]C.Proxy) {
	liveTailscaleMu.Lock()
	defer liveTailscaleMu.Unlock()
	kept := map[string]C.ProxyAdapter{}
	for name, proxy := range proxies {
		wrapper, ok := proxy.(*adapter.Proxy)
		if !ok {
			continue
		}
		node, ok := outbound.UnwrapTailscale(wrapper.ProxyAdapter)
		if !ok {
			continue
		}
		if old, ok := liveTailscale[name]; ok && old != wrapper.ProxyAdapter {
			if oldNode, ok := outbound.UnwrapTailscale(old); ok && !oldNode.Closed() && oldNode.SameConfig(node) {
				// Closing the unstarted replacement drops its DNS client
				// for this name, so the kept node registers itself again.
				_ = wrapper.ProxyAdapter.Close()
				wrapper.ProxyAdapter = old
				oldNode.Adopt()
				log.Infoln("[Tailscale](%s) kept the running node across the reload", name)
			}
		}
		kept[name] = wrapper.ProxyAdapter
	}
	for name, old := range liveTailscale {
		if kept[name] != old {
			log.Infoln("[Tailscale](%s) closed the node the reload replaced", name)
			_ = old.Close()
		}
	}
	liveTailscale = kept
}

// forgetTailscaleNodes drops the record when the core shuts down, which
// closes every proxy itself.
func forgetTailscaleNodes() {
	liveTailscaleMu.Lock()
	liveTailscale = map[string]C.ProxyAdapter{}
	liveTailscaleMu.Unlock()
}

func liveTailscaleNodes() []*outbound.Tailscale {
	liveTailscaleMu.Lock()
	defer liveTailscaleMu.Unlock()
	nodes := make([]*outbound.Tailscale, 0, len(liveTailscale))
	for _, a := range liveTailscale {
		if node, ok := outbound.UnwrapTailscale(a); ok {
			nodes = append(nodes, node)
		}
	}
	return nodes
}
