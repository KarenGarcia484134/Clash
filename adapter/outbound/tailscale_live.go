//go:build with_gvisor && !no_tailscale

// FlClash patch: counts the tsnet nodes that are running in this process,
// so a reload that leaves a replaced node behind shows up in diagnostics and
// tests. Kept in its own file so syncing with upstream never conflicts.

package outbound

import "sync"

var (
	liveTailscaleMu     sync.Mutex
	liveTailscaleNodes  = map[*Tailscale]string{}
	tailscaleNodeStarts int
)

func trackTailscaleStarted(t *Tailscale) {
	liveTailscaleMu.Lock()
	liveTailscaleNodes[t] = t.option.StateDir
	tailscaleNodeStarts++
	liveTailscaleMu.Unlock()
}

func trackTailscaleClosed(t *Tailscale) {
	liveTailscaleMu.Lock()
	delete(liveTailscaleNodes, t)
	liveTailscaleMu.Unlock()
}

// RunningTailscaleNodes is how many tsnet nodes have started and not been
// closed yet.
func RunningTailscaleNodes() int {
	liveTailscaleMu.Lock()
	defer liveTailscaleMu.Unlock()
	return len(liveTailscaleNodes)
}

// TailscaleNodeStarts is how many times a tsnet node has started in this
// process. A reload that keeps the running node does not add to it.
func TailscaleNodeStarts() int {
	liveTailscaleMu.Lock()
	defer liveTailscaleMu.Unlock()
	return tailscaleNodeStarts
}

// SharedTailscaleStateDirs lists every state dir more than one running node
// uses at once. Two nodes on one dir share a machine key and overwrite each
// other's state.
func SharedTailscaleStateDirs() []string {
	liveTailscaleMu.Lock()
	defer liveTailscaleMu.Unlock()
	seen := map[string]int{}
	for _, dir := range liveTailscaleNodes {
		seen[dir]++
	}
	var shared []string
	for dir, count := range seen {
		if count > 1 {
			shared = append(shared, dir)
		}
	}
	return shared
}
