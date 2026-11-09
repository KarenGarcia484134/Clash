// FlClash patch: a provider's tailscale nodes keep their running node across
// reloads and updates. Kept in its own file so syncing with upstream never
// conflicts.

package provider

import (
	"github.com/ProjectClash/Clash/adapter"
	C "github.com/ProjectClash/Clash/constant"
)

// TailscaleOwner is the identity the core records as the owner of this
// provider's tailscale nodes; the wrapper a provider is handed out in
// changes nothing about it.
func (bp *baseProvider) TailscaleOwner() any { return bp }

// reconcileTailscale runs on every list a fetch or update publishes, before
// it is published. Only parsed lists can hold a tailscale node: a node
// library never builds one lazily.
func (bp *baseProvider) reconcileTailscale(list C.ProxyList) {
	if slice, ok := list.(C.ProxySlice); ok {
		adapter.ReconcileTailscale(adapter.TailscaleProviderScope(bp.name), bp, slice)
	}
}
