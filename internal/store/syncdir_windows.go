//go:build windows

package store

// syncDir is a no-op on Windows: NTFS journals directory metadata itself and
// fsyncing a directory handle is not supported (opening one for sync returns
// "Access is denied"). Rename durability is the filesystem's responsibility
// here, the same trade every content-addressed store makes on this platform.
func syncDir(string) error { return nil }
