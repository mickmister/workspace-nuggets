package nuggets

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"regexp"
	"strings"
	"time"
)

var runIDPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,99}$`)
var snapshotPattern = regexp.MustCompile(`^([0-9a-f]{40}|[0-9a-f]{64})$`)

// RunManifest contains identity, not methodology or a second workflow scheduler.
type RunManifest struct {
	SchemaVersion int    `json:"schema_version"`
	ID            string `json:"id"`
	Purpose       string `json:"purpose"`
	Task          string `json:"task,omitempty"`
	CreatedAt     string `json:"created_at"`
}

type WorkflowRun struct {
	Manifest RunManifest `json:"manifest"`
	Resource Resource    `json:"resource"`
}

func runPrefix(id string) (string, error) {
	if !runIDPattern.MatchString(id) {
		return "", invalid("run ID must be 1-100 lowercase letters, digits or hyphens, starting with a letter or digit")
	}
	return "workflow-runs/" + id + "/", nil
}

func runName(id, name string) (string, error) {
	prefix, err := runPrefix(id)
	if err != nil {
		return "", err
	}
	if err := validateName(name); err != nil {
		return "", err
	}
	return prefix + name, nil
}

// runTransaction restricts pinned reads to history reachable from this workspace.
// Neither arbitrary object expressions nor commits from another ref are inputs.
func (s Store) runTransaction(ctx context.Context, snapshot string) (*transaction, string, error) {
	if snapshot != "" && !snapshotPattern.MatchString(snapshot) {
		return nil, "", invalid("snapshot must be a full immutable Git commit ID")
	}
	t, err := s.begin(ctx)
	if err != nil {
		return nil, "", err
	}
	head, err := s.fetch(ctx, t)
	if err == nil && snapshot != "" {
		if head == "" {
			err = &OperationError{Code: "not_found", Message: "workspace has no published snapshot"}
		} else if _, e := t.git(ctx, nil, "merge-base", "--is-ancestor", snapshot, head); e != nil {
			err = &OperationError{Code: "conflict", Message: "snapshot is not reachable in this workspace history"}
		} else {
			head = snapshot
		}
	}
	if err != nil {
		os.RemoveAll(t.dir)
		return nil, "", err
	}
	return t, head, nil
}

func readRunAt(ctx context.Context, t *transaction, head, id string) (*WorkflowRun, error) {
	name, err := runName(id, "run.json")
	if err != nil {
		return nil, err
	}
	doc, err := readAt(ctx, t, head, name)
	if err != nil {
		return nil, err
	}
	var manifest RunManifest
	decoder := json.NewDecoder(strings.NewReader(string(doc.Content)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&manifest); err != nil || manifest.SchemaVersion != 1 || manifest.ID != id || strings.TrimSpace(manifest.Purpose) == "" {
		return nil, invalid("invalid workflow run manifest")
	}
	if _, err := time.Parse(time.RFC3339Nano, manifest.CreatedAt); err != nil {
		return nil, invalid("invalid workflow run creation time")
	}
	return &WorkflowRun{Manifest: manifest, Resource: doc.Resource}, nil
}

func (s Store) InspectRun(ctx context.Context, id, snapshot string) (*WorkflowRun, []Resource, error) {
	prefix, err := runPrefix(id)
	if err != nil {
		return nil, nil, err
	}
	t, head, err := s.runTransaction(ctx, snapshot)
	if err != nil {
		return nil, nil, err
	}
	defer os.RemoveAll(t.dir)
	run, err := readRunAt(ctx, t, head, id)
	if err != nil {
		return nil, nil, err
	}
	all, err := listAt(ctx, t, head)
	resources := []Resource{}
	for _, resource := range all {
		if strings.HasPrefix(resource.Name, prefix) {
			resources = append(resources, resource)
		}
	}
	return run, resources, err
}

func (s Store) CreateRun(ctx context.Context, id, purpose, task string) (*WorkflowRun, Receipt, error) {
	name, err := runName(id, "run.json")
	if err != nil {
		return nil, Receipt{}, err
	}
	if strings.TrimSpace(purpose) == "" || len(purpose) > 4000 || len(task) > 1000 {
		return nil, Receipt{}, invalid("run purpose is required (maximum 4000 bytes); task reference maximum is 1000 bytes")
	}
	existing := func() (*WorkflowRun, Receipt, error) {
		run, _, err := s.InspectRun(ctx, id, "")
		if err != nil {
			return nil, Receipt{}, err
		}
		if run.Manifest.Purpose != purpose || run.Manifest.Task != task {
			return nil, Receipt{}, &OperationError{Code: "conflict", Message: "run identity already exists for different inputs"}
		}
		return run, Receipt{Resource: run.Resource}, nil
	}
	run, receipt, err := existing()
	var failure *OperationError
	if err == nil || !errors.As(err, &failure) || failure.Code != "not_found" {
		return run, receipt, err
	}
	manifest := RunManifest{SchemaVersion: 1, ID: id, Purpose: purpose, Task: task, CreatedAt: time.Now().UTC().Format(time.RFC3339Nano)}
	content, _ := json.MarshalIndent(manifest, "", "  ")
	receipt, err = s.UpsertStrict(ctx, name, "", content)
	if errors.As(err, &failure) && failure.Code == "conflict" {
		return existing() // A concurrent identical create uses the original identity.
	}
	if err != nil {
		return nil, receipt, err
	}
	return &WorkflowRun{Manifest: manifest, Resource: receipt.Resource}, receipt, nil
}

func (s Store) ReadRun(ctx context.Context, id, name, snapshot string) (Document, error) {
	fullName, err := runName(id, name)
	if err != nil {
		return Document{}, err
	}
	t, head, err := s.runTransaction(ctx, snapshot)
	if err != nil {
		return Document{}, err
	}
	defer os.RemoveAll(t.dir)
	if _, err := readRunAt(ctx, t, head, id); err != nil {
		return Document{}, err
	}
	return readAt(ctx, t, head, fullName)
}

func (s Store) WriteRun(ctx context.Context, id, name, expected string, content []byte) (Receipt, error) {
	fullName, err := runName(id, name)
	if err != nil {
		return Receipt{}, err
	}
	if name == "run.json" || strings.HasPrefix(name, "run.json/") {
		return Receipt{}, invalid("run.json is reserved for run identity; create with runs create")
	}
	if _, _, err := s.InspectRun(ctx, id, ""); err != nil {
		return Receipt{}, err
	}
	return s.UpsertStrict(ctx, fullName, expected, content)
}
