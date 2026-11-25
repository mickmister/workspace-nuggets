package nuggets

import "context"

func (s Service) executeMedia(ctx context.Context, req Request, binding *WorkspaceBinding) (*MediaResult, error) {
	if binding == nil {
		return nil, invalid("media requires persistent workspace attachment")
	}
	if req.Operation == "media.bind" {
		profile, ok := s.MessageTargets[req.Target]
		if !ok {
			return nil, invalid("named S3 target is not configured")
		}
		if err := bindNugTarget(ctx, s.RegistryPath, binding.Reference, req.Target, "media", profile); err != nil {
			return nil, err
		}
		return &MediaResult{Target: req.Target, Verified: true}, nil
	}
	if req.Target == "" && len(binding.MediaTargets) == 1 {
		for name := range binding.MediaTargets {
			req.Target = name
		}
	}
	profile, ok := binding.MediaTargets[req.Target]
	if !ok {
		return nil, invalid("select an explicitly bound media target")
	}
	store, err := NewMediaStore(binding.Reference, req.Target, profile)
	if err != nil {
		return nil, err
	}
	switch req.Operation {
	case "media.import":
		return store.Import(ctx, req.Name, req.Purpose, req.Category, req.Source, req.ExpectedVersion, req.Content)
	case "media.list":
		return store.List(ctx, req.Token, req.Limit)
	case "media.read":
		return store.Read(ctx, req.Name, req.Snapshot, true)
	case "media.verify":
		return store.Read(ctx, req.Name, req.Snapshot, false)
	default:
		return nil, invalid("unknown media operation")
	}
}
