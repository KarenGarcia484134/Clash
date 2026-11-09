//go:build with_gvisor && !no_tailscale

// FlClash patch: lets the host sign a node out for good. Kept in its own
// file so syncing with upstream never conflicts.

package outbound

import (
	"errors"
	"os"
	"path/filepath"

	C "github.com/ProjectClash/Clash/constant"
)

// StateDir is where the node keeps its login and machine key.
func (t *Tailscale) StateDir() string {
	return t.option.StateDir
}

// ForgetLogin deletes the files the node saved in its state dir, its login
// and machine key among them, so the next node built on that dir logs in
// afresh, as FlClash's reset does by deleting the dir. Only files are
// removed: a dir another node uses below this one stays. The node must be
// closed first, or tsnet writes the state back.
func (t *Tailscale) ForgetLogin() error {
	if !t.Closed() {
		return errors.New("tailscale node is still running")
	}
	dir := filepath.Clean(t.option.StateDir)
	if dir == "" || dir == filepath.Clean(C.Path.HomeDir()) || !C.Path.IsSafePath(dir) {
		return C.Path.ErrNotSafePath(dir)
	}
	entries, err := os.ReadDir(dir)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	var errs []error
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		if err := os.Remove(filepath.Join(dir, entry.Name())); err != nil && !os.IsNotExist(err) {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}
