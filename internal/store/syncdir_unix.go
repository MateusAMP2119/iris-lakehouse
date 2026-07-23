//go:build unix

package store

import "os"

// syncDir fsyncs a directory so a rename into it is durable across a crash: the
// published object name survives even if the OS has not yet flushed the directory
// entry. A close error after a successful sync is still reported.
func syncDir(dir string) error {
	d, err := os.Open(dir) //nolint:gosec // G304: the object-store root is engine-owned.
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
