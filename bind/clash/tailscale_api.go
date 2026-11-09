package clash

import (
	"context"
	"encoding/json"
	"errors"
	"time"
)

// The tailscale exports give the containing app what FlClash's
// tailscale.status and tailscale.ping methods give its UI, with the same
// JSON fields. Each takes and returns JSON text so the bridge stays a
// string in and a string out; a failure is an "error" field, never a
// thrown error.

type tailscaleStatusParams struct {
	Name  string `json:"name"`
	Start bool   `json:"start"`
	// Wait follows a started node until it settles, so a connect does not
	// report the passing NeedsLogin a node with a saved key goes through.
	Wait bool `json:"wait"`
	// Rebind reports a network change to the node first.
	Rebind bool `json:"rebind"`
	// Reset signs the node out as FlClash's reset does: the node is closed
	// and forgotten, its saved login is deleted, and the status is empty.
	// The next reload starts a fresh node, which logs in again.
	Reset bool `json:"reset"`
}

type tailscalePingParams struct {
	Name    string   `json:"name"`
	Targets []string `json:"targets"`
}

type tailscalePingResult struct {
	Target string `json:"target"`
	// LatencyMs is -1 when the ping failed.
	LatencyMs int    `json:"latencyMs"`
	Endpoint  string `json:"endpoint,omitempty"`
	DERPID    int    `json:"derpRegionId,omitempty"`
	DERPCode  string `json:"derpRegionCode,omitempty"`
	PeerRelay string `json:"peerRelay,omitempty"`
	Error     string `json:"error,omitempty"`
}

type tailscalePeer struct {
	ID             string   `json:"id"`
	HostName       string   `json:"hostName"`
	DNSName        string   `json:"dnsName"`
	OS             string   `json:"os"`
	IPs            []string `json:"ips"`
	Online         bool     `json:"online"`
	ExitNode       bool     `json:"exitNode"`
	ExitNodeOption bool     `json:"exitNodeOption"`
	LastSeen       string   `json:"lastSeen,omitempty"`
}

type tailscaleStatusResult struct {
	Started        bool            `json:"started"`
	BackendState   string          `json:"backendState"`
	AuthURL        string          `json:"authUrl,omitempty"`
	Health         []string        `json:"health,omitempty"`
	MagicDNSSuffix string          `json:"magicDnsSuffix"`
	Self           *tailscalePeer  `json:"self"`
	Peers          []tailscalePeer `json:"peers"`
	Error          string          `json:"error,omitempty"`
	// DialerProxy is what the running node's own traffic really goes
	// through; empty means directly.
	DialerProxy string `json:"dialerProxy,omitempty"`
	// DERPRoutes tells, by DERP region code, whether the node's connection
	// to that relay went "direct" or through the "proxy".
	DERPRoutes map[string]string `json:"derpRoutes,omitempty"`
	// ExitNodeError is why the configured exit node could not be set.
	ExitNodeError string `json:"exitNodeError,omitempty"`
}

type tailscaleDERPRoutesResult struct {
	DERPRoutes map[string]string `json:"derpRoutes"`
	Error      string            `json:"error,omitempty"`
}

type tailscaleActionResult struct {
	Nodes int    `json:"nodes,omitempty"`
	Error string `json:"error,omitempty"`
}

type tailscaleNode struct {
	Name string `json:"name"`
	// Provider is the proxy provider the node came from; empty for one in
	// the proxies section.
	Provider string `json:"provider,omitempty"`
	Started  bool   `json:"started"`
}

type tailscaleNodesResult struct {
	Nodes []tailscaleNode `json:"nodes"`
	Error string          `json:"error,omitempty"`
}

var errTailscaleNodeMissing = errors.New("tailscale node not found")

const (
	tailscaleStatusTimeout    = 15 * time.Second
	tailscaleSettleTimeout    = 10 * time.Second
	tailscalePingTimeout      = 5 * time.Second
	tailscalePingParallel     = 8
	tailscaleReconnectTimeout = 15 * time.Second
)

func tailscaleErrorJSON(err error) string {
	return mustJSON(map[string]string{"error": err.Error()})
}

// TailscaleStatus takes {"name","start","wait","rebind","reset"} and returns
// the node's status as FlClash's tailscale.status does.
func TailscaleStatus(paramsJSON string) string {
	return bridgeSafeString(recoverAppCallValueOr("TailscaleStatus", tailscaleErrorJSON(errors.New("tailscale status panicked")), func() string {
		var params tailscaleStatusParams
		if err := json.Unmarshal([]byte(paramsJSON), &params); err != nil {
			return mustJSON(tailscaleStatusResult{Peers: []tailscalePeer{}, Error: err.Error()})
		}
		ctx, cancel := context.WithTimeout(context.Background(), tailscaleStatusTimeout)
		defer cancel()
		result, err := tailscaleStatus(ctx, params)
		if err != nil {
			result.Error = err.Error()
		}
		if result.Peers == nil {
			result.Peers = []tailscalePeer{}
		}
		return mustJSON(result)
	}))
}

// TailscalePing takes {"name","targets"} and pings every target through
// the node, eight at a time, returning one result per target in order.
func TailscalePing(paramsJSON string) string {
	return bridgeSafeString(recoverAppCallValueOr("TailscalePing", tailscaleErrorJSON(errors.New("tailscale ping panicked")), func() string {
		var params tailscalePingParams
		if err := json.Unmarshal([]byte(paramsJSON), &params); err != nil {
			return tailscaleErrorJSON(err)
		}
		results := make([]tailscalePingResult, len(params.Targets))
		sem := make(chan struct{}, tailscalePingParallel)
		done := make(chan struct{})
		for i, target := range params.Targets {
			go func() {
				sem <- struct{}{}
				defer func() { <-sem; done <- struct{}{} }()
				ctx, cancel := context.WithTimeout(context.Background(), tailscalePingTimeout)
				defer cancel()
				results[i] = tailscalePing(ctx, params.Name, target)
			}()
		}
		for range params.Targets {
			<-done
		}
		return mustJSON(results)
	}))
}

// TailscaleDERPRoutes returns {"derpRoutes":{region code: "direct" or
// "proxy"}} for the named node: how it last reached each DERP relay.
func TailscaleDERPRoutes(name string) string {
	return bridgeSafeString(recoverAppCallValueOr("TailscaleDERPRoutes", tailscaleErrorJSON(errors.New("tailscale derp routes panicked")), func() string {
		routes, err := tailscaleDERPRoutes(name)
		result := tailscaleDERPRoutesResult{DERPRoutes: routes}
		if result.DERPRoutes == nil {
			result.DERPRoutes = map[string]string{}
		}
		if err != nil {
			result.Error = err.Error()
		}
		return mustJSON(result)
	}))
}

// TailscaleNetworkChanged tells every running node the network changed, so
// it rebinds now instead of at tsnet's own next check. Returns {"nodes":n}.
func TailscaleNetworkChanged() string {
	return bridgeSafeString(recoverAppCallValueOr("TailscaleNetworkChanged", tailscaleErrorJSON(errors.New("tailscale network change panicked")), func() string {
		return mustJSON(tailscaleActionResult{Nodes: tailscaleNetworkChanged()})
	}))
}

// TailscaleReconnect makes the named node log in again now when it is stuck
// logged out without a login URL, as tailscale down and up would. Returns
// {} or {"error"}.
func TailscaleReconnect(name string) string {
	return bridgeSafeString(recoverAppCallValueOr("TailscaleReconnect", tailscaleErrorJSON(errors.New("tailscale reconnect panicked")), func() string {
		ctx, cancel := context.WithTimeout(context.Background(), tailscaleReconnectTimeout)
		defer cancel()
		result := tailscaleActionResult{}
		if err := tailscaleReconnect(ctx, name); err != nil {
			result.Error = err.Error()
		}
		return mustJSON(result)
	}))
}

// TailscaleNodes lists the tailscale proxies of the running configuration,
// those its proxy providers bring included, so the host can show them
// without parsing the profile: {"nodes":[{"name","provider","started"}]}.
func TailscaleNodes() string {
	return bridgeSafeString(recoverAppCallValueOr("TailscaleNodes", tailscaleErrorJSON(errors.New("tailscale nodes panicked")), func() string {
		nodes := tailscaleNodes()
		if nodes == nil {
			nodes = []tailscaleNode{}
		}
		return mustJSON(tailscaleNodesResult{Nodes: nodes})
	}))
}
