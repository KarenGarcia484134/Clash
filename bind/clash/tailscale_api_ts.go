//go:build with_gvisor && !no_tailscale

package clash

import (
	"context"
	"errors"
	"net/netip"
	"sort"
	"time"

	"github.com/ProjectClash/Clash/adapter/outbound"
	"github.com/ProjectClash/Clash/tunnel"
	"github.com/metacubex/tailscale/ipn/ipnstate"
	"github.com/metacubex/tailscale/tailcfg"
)

// findTailscaleNode looks the node up among the running proxies first and
// then in the proxy providers.
func findTailscaleNode(name string) (*outbound.Tailscale, bool) {
	if proxy, ok := tunnel.Proxies()[name]; ok && proxy != nil {
		if node, ok := outbound.UnwrapTailscale(proxy.Adapter()); ok {
			return node, true
		}
	}
	for _, provider := range tunnel.Providers() {
		for _, proxy := range provider.Proxies() {
			if proxy != nil && proxy.Name() == name {
				if node, ok := outbound.UnwrapTailscale(proxy.Adapter()); ok {
					return node, true
				}
			}
		}
	}
	return nil, false
}

// tailscalePing sends one disco ping, which shows whether the path is direct
// or relayed. A disco ping cannot get through where WireGuard does not run,
// so a failed one is retried once as TSMP, which only measures the latency.
func tailscalePing(ctx context.Context, name, target string) tailscalePingResult {
	result := tailscalePingResult{Target: target, LatencyMs: -1}
	ip, err := netip.ParseAddr(target)
	if err != nil {
		result.Error = err.Error()
		return result
	}
	node, ok := findTailscaleNode(name)
	if !ok {
		result.Error = errTailscaleNodeMissing.Error()
		return result
	}
	res, err := node.Ping(ctx, ip, tailcfg.PingDisco)
	if (err != nil || res.Err != "") && ctx.Err() == nil {
		if tsmp, tsmpErr := node.Ping(ctx, ip, tailcfg.PingTSMP); tsmpErr == nil && tsmp.Err == "" {
			res, err = tsmp, nil
		}
	}
	if err != nil {
		result.Error = err.Error()
		return result
	}
	return convertTailscalePing(target, res)
}

func convertTailscalePing(target string, res *ipnstate.PingResult) tailscalePingResult {
	result := tailscalePingResult{
		Target:    target,
		LatencyMs: -1,
		Endpoint:  res.Endpoint,
		DERPID:    res.DERPRegionID,
		DERPCode:  res.DERPRegionCode,
		PeerRelay: res.PeerRelay,
		Error:     res.Err,
	}
	if res.Err == "" {
		ms := int(res.LatencySeconds*1000 + 0.5)
		result.LatencyMs = max(ms, 1)
	}
	return result
}

func tailscaleStatus(ctx context.Context, params tailscaleStatusParams) (tailscaleStatusResult, error) {
	node, ok := findTailscaleNode(params.Name)
	if !ok {
		return tailscaleStatusResult{}, errTailscaleNodeMissing
	}
	if params.Start && !node.Started() {
		if err := node.Start(ctx); err != nil {
			return tailscaleStatusResult{Started: node.Started(), DialerProxy: node.DialerProxy()}, err
		}
	} else if params.Start {
		// A connect on a node that is already up but stuck logged out after
		// a failed control dial retries the login now: on a fresh network,
		// with whatever dialer-proxy the node has now.
		node.NetworkChanged()
		if err := node.Reconnect(ctx); err != nil {
			return tailscaleStatusResult{Started: true, DialerProxy: node.DialerProxy()}, err
		}
	}
	if params.Rebind {
		node.NetworkChanged()
	}
	if params.Wait && node.Started() {
		waitCtx, cancel := context.WithTimeout(ctx, tailscaleSettleTimeout)
		_, _, _ = node.WaitSettled(waitCtx)
		cancel()
	}
	status, err := node.Status(ctx)
	if errors.Is(err, outbound.ErrTailscaleNotStarted) {
		return tailscaleStatusResult{DialerProxy: node.DialerProxy()}, nil
	}
	if err != nil {
		return tailscaleStatusResult{Started: true, DialerProxy: node.DialerProxy()}, err
	}
	result := convertTailscaleStatus(status)
	result.DialerProxy = node.DialerProxy()
	result.DERPRoutes = node.DERPRoutes()
	result.ExitNodeError = node.ExitNodeError()
	return result, nil
}

func convertTailscaleStatus(status *ipnstate.Status) tailscaleStatusResult {
	result := tailscaleStatusResult{
		Started:        true,
		BackendState:   status.BackendState,
		AuthURL:        status.AuthURL,
		Health:         status.Health,
		MagicDNSSuffix: status.MagicDNSSuffix,
	}
	if status.CurrentTailnet != nil && status.CurrentTailnet.MagicDNSSuffix != "" {
		result.MagicDNSSuffix = status.CurrentTailnet.MagicDNSSuffix
	}
	if status.Self != nil {
		self := convertTailscalePeer(status.Self)
		if len(self.IPs) == 0 {
			for _, ip := range status.TailscaleIPs {
				self.IPs = append(self.IPs, ip.String())
			}
		}
		result.Self = &self
	}
	for _, peer := range status.Peer {
		if peer != nil {
			result.Peers = append(result.Peers, convertTailscalePeer(peer))
		}
	}
	sort.SliceStable(result.Peers, func(i, j int) bool {
		a, b := result.Peers[i], result.Peers[j]
		if a.Online != b.Online {
			return a.Online
		}
		return a.HostName < b.HostName
	})
	return result
}

func convertTailscalePeer(peer *ipnstate.PeerStatus) tailscalePeer {
	item := tailscalePeer{
		ID:             string(peer.ID),
		HostName:       peer.HostName,
		DNSName:        peer.DNSName,
		OS:             peer.OS,
		Online:         peer.Online,
		ExitNode:       peer.ExitNode,
		ExitNodeOption: peer.ExitNodeOption,
		IPs:            []string{},
	}
	for _, ip := range peer.TailscaleIPs {
		item.IPs = append(item.IPs, ip.String())
	}
	if !peer.LastSeen.IsZero() {
		item.LastSeen = peer.LastSeen.UTC().Format(time.RFC3339)
	}
	return item
}

func tailscaleDERPRoutes(name string) (map[string]string, error) {
	node, ok := findTailscaleNode(name)
	if !ok {
		return nil, errTailscaleNodeMissing
	}
	return node.DERPRoutes(), nil
}

// tailscaleNetworkChanged reaches the nodes the core is running, plus any
// in the current proxies the reconcile has not recorded yet.
func tailscaleNetworkChanged() int {
	seen := map[*outbound.Tailscale]bool{}
	for _, node := range liveTailscaleNodes() {
		seen[node] = true
	}
	for _, proxy := range tunnel.Proxies() {
		if proxy == nil {
			continue
		}
		if node, ok := outbound.UnwrapTailscale(proxy.Adapter()); ok {
			seen[node] = true
		}
	}
	count := 0
	for node := range seen {
		if node.Started() && !node.Closed() {
			node.NetworkChanged()
			count++
		}
	}
	return count
}

func tailscaleReconnect(ctx context.Context, name string) error {
	node, ok := findTailscaleNode(name)
	if !ok {
		return errTailscaleNodeMissing
	}
	return node.Reconnect(ctx)
}
