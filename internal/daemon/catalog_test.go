package daemon

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/MateusAMP2119/iris-lakehouse/internal/api"
	"github.com/MateusAMP2119/iris-lakehouse/internal/catalog"
)

// TestCatalogOrchestratorInstall proves the leader-side pack install: materialize into the
// workspace, answer the derived apply order, and (with apply) run the declare sequence in order.
func TestCatalogOrchestratorInstall(t *testing.T) {
	t.Run("an unknown pack refuses", func(t *testing.T) {
		o := newCatalogOrchestrator(t.TempDir(), nil, nil, nil)
		if _, err := o.installPack(context.Background(), api.CatalogInstallRequest{Pack: "nope"}); err == nil || !strings.Contains(err.Error(), "no such pack") {
			t.Fatalf("installPack = %v, want the no-such-pack refusal", err)
		}
	})

	t.Run("materialize answers files and order without applying", func(t *testing.T) {
		ws := t.TempDir()
		o := newCatalogOrchestrator(ws, nil, nil, nil)
		res, err := o.installPack(context.Background(), api.CatalogInstallRequest{Pack: catalog.StarterPack})
		if err != nil {
			t.Fatalf("installPack: %v", err)
		}
		if res.Applied || len(res.Files) == 0 || len(res.ApplyOrder) != 3 {
			t.Fatalf("result = %+v, want files, a 3-step order, and applied=false", res)
		}
		if _, err := os.Stat(filepath.Join(ws, "pipelines/healthy/quake_feed/iris-declare.yaml")); err != nil {
			t.Errorf("materialized declaration missing: %v", err)
		}
	})

	t.Run("apply runs the declare sequence in the derived order", func(t *testing.T) {
		var applied []string
		fake := func(_ context.Context, req api.ControlRequest) (api.ControlResult, error) {
			applied = append(applied, req.Path)
			return api.ControlResult{Warnings: []string{"w:" + req.Path}}, nil
		}
		o := newCatalogOrchestrator(t.TempDir(), nil, fake, nil)
		res, err := o.installPack(context.Background(), api.CatalogInstallRequest{Pack: catalog.StarterPack, Apply: true})
		if err != nil {
			t.Fatalf("installPack: %v", err)
		}
		if !res.Applied || len(applied) != 3 || len(res.Warnings) != 3 {
			t.Fatalf("applied %v (warnings %d), want the 3-step sequence with aggregated warnings", applied, len(res.Warnings))
		}
		for i, p := range res.ApplyOrder {
			if applied[i] != p {
				t.Fatalf("apply sequence %v diverges from the answered order %v", applied, res.ApplyOrder)
			}
		}
	})

	t.Run("an apply failure names the failing target", func(t *testing.T) {
		fail := errors.New("boom")
		fake := func(_ context.Context, req api.ControlRequest) (api.ControlResult, error) {
			if strings.Contains(req.Path, "quake_report") {
				return api.ControlResult{}, fail
			}
			return api.ControlResult{}, nil
		}
		o := newCatalogOrchestrator(t.TempDir(), nil, fake, nil)
		_, err := o.installPack(context.Background(), api.CatalogInstallRequest{Pack: catalog.StarterPack, Apply: true})
		if err == nil || !strings.Contains(err.Error(), "quake_report") || !errors.Is(err, fail) {
			t.Fatalf("installPack = %v, want the failing target named", err)
		}
	})

	t.Run("a reinstall refuses without force and lands with it", func(t *testing.T) {
		ws := t.TempDir()
		o := newCatalogOrchestrator(ws, nil, nil, nil)
		if _, err := o.installPack(context.Background(), api.CatalogInstallRequest{Pack: catalog.StarterPack}); err != nil {
			t.Fatalf("first install: %v", err)
		}
		if _, err := o.installPack(context.Background(), api.CatalogInstallRequest{Pack: catalog.StarterPack}); err == nil || !strings.Contains(err.Error(), "existing path") {
			t.Fatalf("reinstall = %v, want the existing-path refusal", err)
		}
		if _, err := o.installPack(context.Background(), api.CatalogInstallRequest{Pack: catalog.StarterPack, Force: true}); err != nil {
			t.Fatalf("forced reinstall: %v", err)
		}
	})
}
