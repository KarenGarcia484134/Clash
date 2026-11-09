//go:build with_gvisor && !no_tailscale

// FlClash patch: keeps one tsnet node per tailscale proxy across config
// reloads and proxy-provider updates. Kept in its own file so syncing with
// upstream never conflicts.

package adapter

import (
	"sync"

	"github.com/ProjectClash/Clash/adapter/outbound"
	C "github.com/ProjectClash/Clash/constant"
	"github.com/ProjectClash/Clash/log"
)

// Every reload parses fresh tailscale outbounds, and so does every update
// of a proxy provider; nothing closed the one each replaced, only the
// finalizer on its wrapper did, at some later GC. Until then two tsnet
// nodes ran on one state dir with one machine key, and every reload or
// update restarted the node. The registry remembers the running node of
// every tailscale proxy, by scope (TailscaleConfigScope for the proxies
// section, TailscaleProviderScope(name) for a provider) and name, so the
// next parse of the same scope can keep an unchanged node and close a
// changed or dropped one at once.

const TailscaleConfigScope = ""

func TailscaleProviderScope(name string) string { return "provider:" + name }

var (
	tailscaleRegistryMu sync.Mutex
	tailscaleRegistry   = map[string]map[string]C.ProxyAdapter{}
	// tailscaleOwners records which provider object may reconcile a
	// provider scope, so the fetcher of a provider a reload replaced, which
	// runs until a GC closes it, cannot close the nodes of the new one.
	tailscaleOwners = map[string]any{}
)

// ClaimTailscaleScopes makes owners the only providers that reconcile their
// scopes, and closes the nodes of every provider scope not among them.
func ClaimTailscaleScopes(owners map[string]any) {
	tailscaleRegistryMu.Lock()
	defer tailscaleRegistryMu.Unlock()
	tailscaleOwners = map[string]any{}
	for scope, owner := range owners {
		tailscaleOwners[scope] = owner
	}
	for scope, nodes := range tailscaleRegistry {
		if scope == TailscaleConfigScope {
			continue
		}
		if _, ok := owners[scope]; ok {
			continue
		}
		for name, old := range nodes {
			log.Infoln("[Tailscale](%s) closed the node of the provider the reload dropped", name)
			_ = old.Close()
		}
		delete(tailscaleRegistry, scope)
	}
}

// ReconcileTailscale runs on a parsed list before anything can use it: an
// unchanged node takes over the running one, and every node the scope ran
// before that is not kept is closed. owner is the provider the list is
// for, or nil for the proxies section; a provider that does not own its
// scope is ignored.
func ReconcileTailscale(scope string, owner any, proxies []C.Proxy) {
	tailscaleRegistryMu.Lock()
	defer tailscaleRegistryMu.Unlock()
	if owner != nil {
		if current, ok := tailscaleOwners[scope]; ok && current != owner {
			return
		}
		tailscaleOwners[scope] = owner
	}
	live := tailscaleRegistry[scope]
	kept := map[string]C.ProxyAdapter{}
	for _, proxy := range proxies {
		wrapper, ok := proxy.(*Proxy)
		if !ok || wrapper == nil {
			continue
		}
		node, ok := outbound.UnwrapTailscale(wrapper.ProxyAdapter)
		if !ok {
			continue
		}
		name := wrapper.Name()
		if old, ok := live[name]; ok && old != wrapper.ProxyAdapter {
			if oldNode, ok := outbound.UnwrapTailscale(old); ok && !oldNode.Closed() && oldNode.SameConfig(node) {
				// Closing the unstarted replacement drops its DNS client
				// for this name, so the kept node registers itself again.
				_ = wrapper.ProxyAdapter.Close()
				wrapper.ProxyAdapter = old
				oldNode.Adopt()
				log.Infoln("[Tailscale](%s) kept the running node", name)
			}
		}
		kept[name] = wrapper.ProxyAdapter
	}
	for name, old := range live {
		if kept[name] != old {
			log.Infoln("[Tailscale](%s) closed the node that was replaced", name)
			_ = old.Close()
		}
	}
	if len(kept) == 0 {
		delete(tailscaleRegistry, scope)
		return
	}
	tailscaleRegistry[scope] = kept
}

// ForgetTailscale drops every record when the core shuts down, which
// closes every proxy itself.
func ForgetTailscale() {
	tailscaleRegistryMu.Lock()
	tailscaleRegistry = map[string]map[string]C.ProxyAdapter{}
	tailscaleOwners = map[string]any{}
	tailscaleRegistryMu.Unlock()
}

// RetireTailscaleNode closes node and forgets it, so the next reload or
// update starts a fresh node in its place instead of keeping this one.
func RetireTailscaleNode(node *outbound.Tailscale) {
	tailscaleRegistryMu.Lock()
	for scope, nodes := range tailscaleRegistry {
		for name, old := range nodes {
			if n, ok := outbound.UnwrapTailscale(old); ok && n == node {
				delete(nodes, name)
			}
		}
		if len(nodes) == 0 {
			delete(tailscaleRegistry, scope)
		}
	}
	tailscaleRegistryMu.Unlock()
	_ = node.Close()
}

// LiveTailscaleNodes lists the nodes the registry holds.
func LiveTailscaleNodes() []*outbound.Tailscale {
	tailscaleRegistryMu.Lock()
	defer tailscaleRegistryMu.Unlock()
	var nodes []*outbound.Tailscale
	for _, scope := range tailscaleRegistry {
		for _, a := range scope {
			if node, ok := outbound.UnwrapTailscale(a); ok {
				nodes = append(nodes, node)
			}
		}
	}
	return nodes
}
