//go:build windows

package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/windows/registry"
)

// removeSelfBinary removes the running binary at path. Windows refuses to
// delete a running executable but allows renaming it, so the binary moves
// aside into the user temp directory; the leftover is unlocked once this
// process exits and is swept with the OS temp store.
func removeSelfBinary(path string) error {
	if err := os.Remove(path); err == nil {
		return nil
	}
	aside := filepath.Join(os.TempDir(), fmt.Sprintf("iris-uninstalled-%d.exe", os.Getpid()))
	_ = os.Remove(aside)
	if err := os.Rename(path, aside); err != nil {
		return fmt.Errorf("move running executable aside: %w", err)
	}
	return nil
}

// removeUserPathEntry strips dir from the user PATH in the registry, undoing
// install.ps1's persistent PATH entry. Best-effort: a PATH left in place only
// leaves a dangling entry behind.
func removeUserPathEntry(dir string) {
	key, err := registry.OpenKey(registry.CURRENT_USER, "Environment", registry.QUERY_VALUE|registry.SET_VALUE)
	if err != nil {
		return
	}
	defer func() { _ = key.Close() }()
	current, typ, err := key.GetStringValue("Path")
	if err != nil {
		return
	}
	var kept []string
	removed := false
	for _, entry := range strings.Split(current, ";") {
		if strings.EqualFold(filepath.Clean(entry), filepath.Clean(dir)) {
			removed = true
			continue
		}
		kept = append(kept, entry)
	}
	if !removed {
		return
	}
	if typ == registry.EXPAND_SZ {
		_ = key.SetExpandStringValue("Path", strings.Join(kept, ";"))
		return
	}
	_ = key.SetStringValue("Path", strings.Join(kept, ";"))
}
