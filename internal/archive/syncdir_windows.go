//go:build windows

package archive

// syncDir is a no-op on Windows: fsyncing a directory handle is not supported
// there ("Access is denied"), and NTFS journals directory metadata itself.
// Matches internal/store's Windows behavior.
func syncDir(string) error { return nil }
