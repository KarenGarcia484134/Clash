//go:build !with_gvisor || no_tailscale

package adapter

import C "github.com/ProjectClash/Clash/constant"

const TailscaleConfigScope = ""

func TailscaleProviderScope(name string) string { return "provider:" + name }

func ClaimTailscaleScopes(map[string]any) {}

func ReconcileTailscale(string, any, []C.Proxy) {}

func ForgetTailscale() {}
