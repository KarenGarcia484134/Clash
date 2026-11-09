//go:build !with_gvisor || no_tailscale

package clash

import (
	C "github.com/ProjectClash/Clash/constant"
	P "github.com/ProjectClash/Clash/constant/provider"
)

func reconcileTailscaleNodes(map[string]C.Proxy, map[string]P.ProxyProvider) {}

func settleTailscaleProviders(map[string]P.ProxyProvider) {}

func forgetTailscaleNodes() {}
