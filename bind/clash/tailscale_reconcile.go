//go:build with_gvisor && !no_tailscale

package clash

import (
	"github.com/ProjectClash/Clash/adapter"
	"github.com/ProjectClash/Clash/adapter/outbound"
	C "github.com/ProjectClash/Clash/constant"
	P "github.com/ProjectClash/Clash/constant/provider"
)

// Every Start and Reload parses fresh tailscale outbounds, in the proxies
// section and in every proxy provider, and nothing closed the ones they
// replaced: only the finalizer on each wrapper did, at some later GC. Until
// then two tsnet nodes ran on the same state dir with the same machine key,
// and every reload restarted the node, dropping its connections and logging
// in again. reconcileTailscaleNodes runs right before a parsed
// configuration is applied: an unchanged node keeps the running one, by
// giving the new Proxy the old adapter before anything, groups included,
// can use it; a node that changed or is gone is closed now, before its
// replacement can start. A provider that fetches reconciles each list it
// fetches itself (adapter.ReconcileTailscale from setProxyList), on the
// first load and on every update; this records which provider objects own
// those scopes, so one the reload replaced cannot, and closes the nodes of
// providers that are gone.

type tailscaleOwner interface{ TailscaleOwner() any }

func reconcileTailscaleNodes(proxies map[string]C.Proxy, providers map[string]P.ProxyProvider) {
	list := make([]C.Proxy, 0, len(proxies))
	for _, proxy := range proxies {
		list = append(list, proxy)
	}
	adapter.ReconcileTailscale(adapter.TailscaleConfigScope, nil, list)

	owners := map[string]any{}
	for name, provider := range providers {
		if owner, ok := provider.(tailscaleOwner); ok {
			owners[adapter.TailscaleProviderScope(name)] = owner.TailscaleOwner()
		}
	}
	adapter.ClaimTailscaleScopes(owners)
	// An inline provider parsed its list when it was built and never loads
	// it again, so it is reconciled here, before it is published.
	for name, provider := range providers {
		if provider.VehicleType() == P.Inline {
			reconcileTailscaleProvider(name, provider)
		}
	}
}

// settleTailscaleProviders runs once the configuration is applied and every
// provider that loads synchronously has loaded: one whose load failed still
// shows the fallback proxies it was built with, which are reconciled here.
// An empty provider is left alone, since one that loads in the background
// starts empty and reconciles when its list arrives.
func settleTailscaleProviders(providers map[string]P.ProxyProvider) {
	for name, provider := range providers {
		if provider.Count() > 0 {
			reconcileTailscaleProvider(name, provider)
		}
	}
}

func reconcileTailscaleProvider(name string, provider P.ProxyProvider) {
	owner, ok := provider.(tailscaleOwner)
	if !ok {
		return
	}
	if _, ok := provider.ProxyList().(C.ProxySlice); !ok {
		return
	}
	adapter.ReconcileTailscale(adapter.TailscaleProviderScope(name), owner.TailscaleOwner(), provider.Proxies())
}

// forgetTailscaleNodes drops the record when the core shuts down, which
// closes every proxy itself.
func forgetTailscaleNodes() { adapter.ForgetTailscale() }

func liveTailscaleNodes() []*outbound.Tailscale { return adapter.LiveTailscaleNodes() }
