//go:build unix

package archive

import "os"

// syncDir is a local copy of the durable-dir sync (avoid importing internal
// details; small and stdlib).
func syncDir(dir string) error {
	d, err := os.Open(dir) //nolint:gosec // G304: archive-owned dir.
	if err != nil {
		return err
	}
	serr := d.Sync()
	cerr := d.Close()
	if serr != nil {
		return serr
	}
	return cerr
}
