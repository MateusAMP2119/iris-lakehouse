package daemon

import (
	"context"
	"fmt"
	"sort"

	"github.com/MateusAMP2119/iris-lakehouse/internal/api"
	"github.com/MateusAMP2119/iris-lakehouse/internal/pg"
	"github.com/MateusAMP2119/iris-lakehouse/internal/store"
)

// This file is the daemon's GET /schemas plane: the workspace's declared
// tables joined to the pipelines that hold a write grant on them. Both halves
// are declaration truth -- the schemas/ tree and the grants table -- so a
// declared table nobody has written yet still describes itself, and a pipeline
// that has never run still names its output. Nothing here reads the data
// database.

// schemaShapeSource is the declared-shape half of the listing; the workspace
// data source satisfies it.
type schemaShapeSource interface {
	// TableShapes returns every declared table's shape, ordered.
	TableShapes() ([]api.TableShape, bool)
}

// schemaBindingReader is the write-map half: every role's written tables and
// the registered pipelines those roles belong to.
type schemaBindingReader interface {
	// WriteBindings returns every role's written tables, in stable order.
	WriteBindings(ctx context.Context) ([]store.WriteBinding, error)
	// RegisteredPipelines returns every registered pipeline name, in name order.
	RegisteredPipelines(ctx context.Context) ([]string, error)
}

// schemasPlane serves GET /schemas over the workspace tree and the meta grant
// map.
type schemasPlane struct {
	shapes   schemaShapeSource
	bindings schemaBindingReader
}

// compile-time proof the plane satisfies the api seam.
var _ api.SchemaListHandler = (*schemasPlane)(nil)

// NewSchemasPlane builds the declared-shape listing plane. A nil shape source
// serves an empty table set; a nil binding reader serves shapes with no
// outputs, so a partially wired daemon degrades to less detail, never a fault.
func NewSchemasPlane(shapes schemaShapeSource, bindings schemaBindingReader) api.SchemaListHandler {
	return &schemasPlane{shapes: shapes, bindings: bindings}
}

// ListSchemas composes the declared tables and the pipeline write bindings.
// The role-to-pipeline direction is derived forward through PipelineRoleName
// over the registered pipelines: the role name is a one-way derivation, so a
// grant held by a role no pipeline maps to (a data PAT's) is left out rather
// than credited to a pipeline that does not own it.
func (p *schemasPlane) ListSchemas(ctx context.Context) (api.SchemaListResult, error) {
	var out api.SchemaListResult
	if p.shapes != nil {
		if tables, ok := p.shapes.TableShapes(); ok {
			out.Tables = tables
		}
	}
	if p.bindings == nil {
		return out, nil
	}
	pipelines, err := p.bindings.RegisteredPipelines(ctx)
	if err != nil {
		return api.SchemaListResult{}, fmt.Errorf("daemon: read registered pipelines for /schemas: %w", err)
	}
	byRole := make(map[string]string, len(pipelines))
	for _, name := range pipelines {
		byRole[pg.PipelineRoleName(name)] = name
	}
	bindings, err := p.bindings.WriteBindings(ctx)
	if err != nil {
		return api.SchemaListResult{}, fmt.Errorf("daemon: read write bindings for /schemas: %w", err)
	}
	for _, wb := range bindings {
		name, ok := byRole[wb.Role]
		if !ok {
			continue
		}
		out.Outputs = append(out.Outputs, api.PipelineOutput{Pipeline: name, Schema: wb.Schema, Table: wb.Table})
	}
	sort.Slice(out.Outputs, func(a, b int) bool {
		x, y := out.Outputs[a], out.Outputs[b]
		if x.Pipeline != y.Pipeline {
			return x.Pipeline < y.Pipeline
		}
		if x.Schema != y.Schema {
			return x.Schema < y.Schema
		}
		return x.Table < y.Table
	})
	return out, nil
}
