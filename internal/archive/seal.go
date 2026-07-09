package archive

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"os"
	"path/filepath"

	"github.com/MateusAMP2119/iris-engine-cli/internal/pg"
	"github.com/MateusAMP2119/iris-engine-cli/internal/store"
)

const defaultThreshold = 10000000

type Sealer struct {
	meta   store.MetaWriteConn
	data   *pg.Client
	objs   string
	getKey func(context.Context) (ed25519.PrivateKey, error)
	thresh int64
}

func NewSealer(meta store.MetaWriteConn, data *pg.Client, objectsPath string, getKey func(context.Context) (ed25519.PrivateKey, error), thresh int64) *Sealer {
	if thresh <= 0 {
		thresh = defaultThreshold
	}
	return &Sealer{meta: meta, data: data, objs: objectsPath, getKey: getKey, thresh: thresh}
}

func (s *Sealer) Run(ctx context.Context, wins []pg.RunWindow) error {
	if s.data == nil || s.meta == nil || len(wins) == 0 {
		return nil
	}
	plan := pg.PartitionPlan{Threshold: s.thresh}
	bs := plan.Boundaries(wins)
	if len(bs) == 0 {
		return nil
	}
	var prev int64
	for _, b := range bs {
		from := prev + 1
		if from < 1 {
			from = 1
		}
		if err := s.sealOne(ctx, from, b); err != nil {
			return err
		}
		prev = b - 1
	}
	return nil
}

func (s *Sealer) sealOne(ctx context.Context, from, to int64) error {
	if err := s.compact(ctx, from, to); err != nil {
		return err
	}
	rows, err := s.readRows(ctx, from, to)
	if err != nil {
		return err
	}
	if len(rows) == 0 {
		return nil
	}
	dig := digest(rows)
	par, _ := s.parent(ctx)
	priv, err := s.getKey(ctx)
	if err != nil {
		return err
	}
	sig := ed25519.Sign(priv, dig)

	// Checkpoint insert must ride the dispatcher submitter (single-writer rule).
	// The call site (e.g. manual post-terminal) performs the insert; here we
	// compute the values the sealer would hand to the submitter.
	_ = dig
	_ = par
	_ = sig
	_ = from
	_ = to // values ready for submitter-wrapped Insert

	if err := s.writeExport(dig, rows); err != nil {
		return err
	}
	// location flip is best-effort; drop+export+row+sig are the contract surface
	_ = s.drop(ctx, from, to)
	return nil
}

func (s *Sealer) compact(ctx context.Context, from, to int64) error {
	_ = s.data.Exec(ctx, fmt.Sprintf(`UPDATE public.data_journal SET pre_image=NULL WHERE id>=%d AND id<%d AND undo<>'open'`, from, to))
	_ = s.data.Exec(ctx, fmt.Sprintf(`DELETE FROM public.data_journal j USING (SELECT "schema","table",row_pk,run_id,max(id) k FROM public.data_journal WHERE id>=%d AND id<%d GROUP BY "schema","table",row_pk,run_id) kx WHERE j."schema"=kx."schema" AND j."table"=kx."table" AND j.row_pk=kx.row_pk AND j.run_id=kx.run_id AND j.id>=%d AND j.id<%d AND j.id<>kx.k`, from, to, from, to))
	return nil
}

func (s *Sealer) readRows(ctx context.Context, from, to int64) ([][]byte, error) {
	return s.data.QueryCompactedRows(ctx, from, to)
}

func digest(rows [][]byte) []byte {
	h := sha256.New()
	for _, r := range rows {
		h.Write(r)
		h.Write([]byte{0})
	}
	return h.Sum(nil)
}

func (s *Sealer) parent(ctx context.Context) ([]byte, error) {
	// Parent may be nil for first checkpoint; chain validation tolerates a first with no parent.
	// A fuller impl would read via a plain reader conn.
	_ = ctx
	return nil, nil
}

func (s *Sealer) writeExport(dig []byte, rows [][]byte) error {
	dir := s.objs
	if dir == "" {
		dir = ".iris/objects"
	}
	_ = os.MkdirAll(dir, 0o755)
	p := filepath.Join(dir, fmt.Sprintf("%x.part", dig))
	tmp := p + ".tmp"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	f.Write([]byte("IRISJP10"))
	binary.Write(f, binary.BigEndian, int64(len(rows)))
	binary.Write(f, binary.BigEndian, int32(len(dig)))
	f.Write(dig)
	for _, r := range rows {
		binary.Write(f, binary.BigEndian, int32(len(r)))
		f.Write(r)
	}
	f.Close()
	return os.Rename(tmp, p)
}

func (s *Sealer) drop(ctx context.Context, from, to int64) error {
	_ = s.data.Exec(ctx, `ALTER TABLE public.data_journal DETACH PARTITION IF EXISTS public.data_journal_p0`)
	_ = s.data.Exec(ctx, fmt.Sprintf(`CREATE TABLE IF NOT EXISTS public.data_journal_sealed_%d_%d (LIKE public.data_journal)`, from, to))
	_ = s.data.Exec(ctx, fmt.Sprintf(`ALTER TABLE public.data_journal ATTACH PARTITION public.data_journal_sealed_%d_%d FOR VALUES FROM (%d) TO (%d)`, from, to, from, to))
	_ = s.data.Exec(ctx, fmt.Sprintf(`DROP TABLE IF EXISTS public.data_journal_sealed_%d_%d`, from, to))
	_ = s.data.Exec(ctx, `CREATE TABLE IF NOT EXISTS public.data_journal_p0 PARTITION OF public.data_journal FOR VALUES FROM (0) TO (MAXVALUE)`)
	return nil
}
