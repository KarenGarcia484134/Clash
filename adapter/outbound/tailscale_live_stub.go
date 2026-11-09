//go:build !with_gvisor || no_tailscale

package outbound

func RunningTailscaleNodes() int { return 0 }

func SharedTailscaleStateDirs() []string { return nil }

func TailscaleNodeStarts() int { return 0 }
