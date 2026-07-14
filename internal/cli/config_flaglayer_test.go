package cli

import (
	"testing"
)

// This file proves the daemon-scoped numeric flags actually reach the resolved
// settings: --retain and --journal-partition-rows contribute to the flag layer
// when (and only when) the invocation set them, and a non-integer value is
// rejected at flag-parse time rather than silently dropped.

func TestFlagLayerCarriesNumericDaemonFlags(t *testing.T) {
	t.Run("flag-layer-numeric-daemon-flags", func(t *testing.T) {
		t.Run("set flags contribute their values", func(t *testing.T) {
			start := find(find(testRoot(), "engine"), "start")
			if err := start.ParseFlags([]string{"--retain", "42", "--journal-partition-rows", "5000"}); err != nil {
				t.Fatalf("ParseFlags: %v", err)
			}
			l := flagLayer(start)
			if l.Retain == nil || *l.Retain != 42 {
				t.Errorf("flag layer Retain = %v, want 42 (--retain must reach the settings)", l.Retain)
			}
			if l.JournalPartitionRows == nil || *l.JournalPartitionRows != 5000 {
				t.Errorf("flag layer JournalPartitionRows = %v, want 5000", l.JournalPartitionRows)
			}
		})

		t.Run("unset flags contribute nothing (defaults defer to lower layers)", func(t *testing.T) {
			start := find(find(testRoot(), "engine"), "start")
			if err := start.ParseFlags(nil); err != nil {
				t.Fatalf("ParseFlags: %v", err)
			}
			l := flagLayer(start)
			if l.Retain != nil {
				t.Errorf("flag layer Retain = %v for an unset flag, want nil", *l.Retain)
			}
			if l.JournalPartitionRows != nil {
				t.Errorf("flag layer JournalPartitionRows = %v for an unset flag, want nil", *l.JournalPartitionRows)
			}
		})

		t.Run("a non-integer value is rejected at parse time", func(t *testing.T) {
			start := find(find(testRoot(), "engine"), "start")
			if err := start.ParseFlags([]string{"--retain", "many"}); err == nil {
				t.Error("ParseFlags accepted --retain many; a non-integer retention must fail loudly")
			}
		})
	})
}
