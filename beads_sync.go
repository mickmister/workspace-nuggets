package nuggets

import (
	"context"
	"errors"
	"os"
	"path/filepath"
)

type BeadsSynchronization struct {
	Attempts    int               `json:"attempts"`
	Merges      []*BeadsCatchUp   `json:"merges,omitempty"`
	Publication *BeadsPublication `json:"publication,omitempty"`
}

// SyncBeads is the ordinary peer synchronization operation. PublishBeads stays
// push-only for callers that explicitly need rejection rather than merging.
// A non-fast-forward push triggers native fetch + clean merge, never resolving
// pull. Fetch, identity and real merge failures stop; publication retries are bounded.
func (s Store) SyncBeads(ctx context.Context, root string) (*BeadsSynchronization, error) {
	result := &BeadsSynchronization{}
	for attempt := 1; ; attempt++ {
		result.Attempts = attempt
		published, err := s.PublishBeads(ctx, root)
		if published != nil {
			result.Publication = published
		}
		if err == nil {
			return result, nil
		}
		var failure *OperationError
		if !errors.As(err, &failure) || failure.Code != "push_failed" || attempt == 3 {
			return result, err
		}
		info, err := s.InspectBeads(ctx, root)
		if err != nil {
			return result, err
		}
		t, err := s.begin(ctx)
		if err != nil {
			return result, err
		}
		clone := filepath.Join(t.dir, "sync-peer")
		_, err = runNativeDolt(ctx, t.dir, "clone", "--ref", info.DesiredRef, info.DesiredURL, clone)
		var head string
		if err == nil {
			head, err = verifyNativeCheckpoint(ctx, clone, info.CollectionID, "")
		}
		os.RemoveAll(t.dir)
		if err != nil {
			return result, err
		}
		merged, err := s.CatchUpBeads(ctx, root, head)
		if merged != nil {
			result.Merges = append(result.Merges, merged)
		}
		if err != nil {
			return result, err
		}
	}
}
