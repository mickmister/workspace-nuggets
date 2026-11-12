package nuggets

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"time"

	"github.com/gofrs/flock"
)

func bindMessageTarget(ctx context.Context, registryPath, reference, name string, target MessageTarget) error {
	return bindNugTarget(ctx, registryPath, reference, name, "messages", target)
}
func bindNugTarget(ctx context.Context, registryPath, reference, name, kind string, target MessageTarget) error {
	if err := validateMessageLabel(name); err != nil {
		return err
	}
	if err := target.validateWrite(); err != nil {
		return err
	}
	lock := flock.New(registryPath + ".lock")
	defer lock.Close()
	locked, err := lock.TryLockContext(ctx, 20*time.Millisecond)
	if err != nil {
		return err
	}
	if !locked {
		return ctx.Err()
	}
	r, err := readRegistry(registryPath)
	if err != nil {
		return err
	}
	for i := range r.Bindings {
		if r.Bindings[i].Reference != reference {
			continue
		}
		targets := &r.Bindings[i].MessageTargets
		if kind == "media" {
			targets = &r.Bindings[i].MediaTargets
		}
		if previous, ok := (*targets)[name]; ok {
			if previous != target {
				return &OperationError{Code: "conflict", Message: "message target already bound; config changes do not retarget existing bindings"}
			}
			return nil
		}
		if *targets == nil {
			*targets = map[string]MessageTarget{}
		}
		(*targets)[name] = target
		return saveJSON(registryPath, r)
	}
	return invalid("message binding requires a registered workspace")
}

func (s Service) executeMessages(ctx context.Context, req Request, binding *WorkspaceBinding, root string) (*MessageResult, error) {
	if binding == nil {
		return nil, invalid("message operations require a persistent workspace attachment")
	}
	if req.Operation == "messages.bind" {
		profile, ok := s.MessageTargets[req.Target]
		if !ok {
			return nil, invalid("named message target is not configured")
		}
		if err := bindMessageTarget(ctx, s.RegistryPath, binding.Reference, req.Target, profile); err != nil {
			return nil, err
		}
		return &MessageResult{Target: req.Target, Verified: true}, nil
	}
	if req.Target == "" && len(binding.MessageTargets) == 1 {
		for name := range binding.MessageTargets {
			req.Target = name
		}
	}
	profile, ok := binding.MessageTargets[req.Target]
	if !ok {
		return nil, invalid("select an explicitly bound message target; use messages bind first")
	}
	store, err := NewMessageStore(binding.Reference, binding.Workspace, req.Target, binding.CacheDir, profile)
	if err != nil {
		return nil, err
	}
	filter := MessageFilter{Session: req.Session, Role: req.Role}
	result := &MessageResult{Target: req.Target}
	switch req.Operation {
	case "messages.compare":
		other, ok := binding.MessageTargets[req.OtherTarget]
		if !ok {
			return result, invalid("comparison requires another explicitly bound target")
		}
		peer, err := NewMessageStore(binding.Reference, binding.Workspace, req.OtherTarget, binding.CacheDir, other)
		if err != nil {
			return result, err
		}
		left, err := store.Query(ctx, "", filter, "")
		if err != nil {
			return result, err
		}
		right, err := peer.Query(ctx, "", filter, "")
		if err != nil {
			return result, err
		}
		result.Comparison = map[string]string{req.Target: left.Digest, req.OtherTarget: right.Digest}
		result.Verified = left.Digest == right.Digest
		if !result.Verified {
			return result, &OperationError{Code: "semantic_mismatch", Message: "selected message targets differ; no automatic copying or winner selection performed"}
		}
	case "messages.scan", "messages.sync":
		if req.Root != "" && req.Root != "." {
			selected, err := canonicalDirectory(req.Root)
			if err != nil {
				return result, err
			}
			if selected != root {
				return result, invalid("native scan root must match selected workspace attachment")
			}
		}
		catalog, _, _, err := store.root(ctx, "", true)
		if err != nil {
			return result, err
		}
		batch, err := ScanOpenCodeMessages(ctx, binding.Reference, OpenCodeMessageSource{Database: req.Database, Version: req.NativeVersion, Root: root, Members: req.Repositories, Aliases: req.Aliases, Session: req.Session, Limit: req.Limit})
		if err != nil {
			return result, err
		}
		blocks := newNugBlocks(store.catalog)
		for i := range batch.Sessions {
			descriptor, _, err := getMessageDescriptor(ctx, blocks, catalog.Sessions, batch.Sessions[i].ID)
			if err != nil {
				return result, err
			}
			batch.Sessions[i].BaseRevision = descriptor.Revision
		}
		if req.VKDatabase != "" {
			if err := VerifyVKMessageSessions(ctx, req.VKDatabase, req.VKWorkspace, &batch); err != nil {
				return result, err
			}
		}
		if req.Operation == "messages.scan" {
			result.Batch = &batch
			return result, nil
		}
		result.Publication, err = store.Publish(ctx, batch)
		if err != nil {
			return result, err
		}
		// Reconstruction metadata is committed only after verified publication.
		index := messageLocalObjects{filepath.Join(binding.CacheDir, "messages", binding.Reference, req.Target, "index.sqlite")}
		for _, session := range batch.Sessions {
			key := "sources/" + messageHash([]byte(session.ID))
			_, version, readErr := index.get(ctx, key)
			if readErr != nil {
				return result, readErr
			}
			metadata := struct {
				Session       string `json:"session"`
				SourceUpdated int64  `json:"source_updated"`
				Snapshot      string `json:"snapshot"`
			}{session.ID, session.Updated, result.Publication.Snapshot}
			_, err := index.cas(ctx, key, version, encodeMessageJSON(metadata), nil)
			if err != nil && !errors.Is(err, errMessageCAS) {
				return result, err
			}
		}
	case "messages.import":
		var batch MessageBatch
		if err := json.Unmarshal(req.Content, &batch); err != nil {
			return result, invalid("message import requires a normalized MessageBatch JSON")
		}
		result.Publication, err = store.Publish(ctx, batch)
	case "messages.sessions":
		return store.Sessions(ctx, req.Snapshot, filter.Session)
	case "messages.read":
		return store.Query(ctx, req.Snapshot, filter, req.Name)
	case "messages.verify":
		return store.Verify(ctx, req.Snapshot, filter)
	case "messages.prepare", "messages.catch-up":
		result.Page, err = store.Prepare(ctx, req.Reader, filter, req.Limit)
		if err == nil && req.Operation == "messages.catch-up" && result.Page.Token != "" {
			result.Page, err = store.ReadPage(ctx, req.Reader, filter, result.Page.Token)
		}
	case "messages.read-page":
		result.Page, err = store.ReadPage(ctx, req.Reader, filter, req.Token)
	case "messages.ack":
		result.Reader, err = store.Ack(ctx, req.Reader, filter, req.Token)
	case "messages.reader":
		state, _, readErr := store.loadReader(ctx, req.Reader, filter)
		result.Reader, err = readerView(state), readErr
	default:
		err = invalid("unknown message operation")
	}
	return result, err
}

func defaultOpenCodeDatabase() string {
	base := os.Getenv("XDG_DATA_HOME")
	if base == "" {
		home, _ := os.UserHomeDir()
		base = filepath.Join(home, ".local", "share")
	}
	return filepath.Join(base, "opencode", "opencode.db")
}
