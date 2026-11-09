//go:build !with_gvisor || no_tailscale

package clash

import C "github.com/ProjectClash/Clash/constant"

func reconcileTailscaleNodes(map[string]C.Proxy) {}

func forgetTailscaleNodes() {}
