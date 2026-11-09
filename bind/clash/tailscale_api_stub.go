//go:build !with_gvisor || no_tailscale

package clash

import (
	"context"
	"errors"
)

var errTailscaleNotBuiltIn = errors.New("tailscale is not built in")

func tailscaleStatus(context.Context, tailscaleStatusParams) (tailscaleStatusResult, error) {
	return tailscaleStatusResult{}, errTailscaleNotBuiltIn
}

func tailscalePing(_ context.Context, _ string, target string) tailscalePingResult {
	return tailscalePingResult{Target: target, LatencyMs: -1, Error: errTailscaleNotBuiltIn.Error()}
}

func tailscaleDERPRoutes(string) (map[string]string, error) { return nil, errTailscaleNotBuiltIn }

func tailscaleNetworkChanged() int { return 0 }

func tailscaleReconnect(context.Context, string) error { return errTailscaleNotBuiltIn }

func tailscaleNodes() []tailscaleNode { return nil }
