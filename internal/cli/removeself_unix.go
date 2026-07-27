//go:build unix

package cli

import "os"

// removeSelfBinary removes the running binary at path. On unix an open
// executable can be unlinked directly.
func removeSelfBinary(path string) error { return os.Remove(path) }

// removeUserPathEntry is a no-op on unix: install.sh wires PATH through shell
// rc files, which removeShellPathEntries already cleans.
func removeUserPathEntry(string) {}
