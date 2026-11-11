package nuggets

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

type Config struct {
	Engagement     string                   `json:"engagement,omitempty"`
	Remote         string                   `json:"remote,omitempty"`
	CacheDir       string                   `json:"cache_dir,omitempty"`
	MessageTargets map[string]MessageTarget `json:"message_targets,omitempty"`
}

func ConfigPath() (string, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "nugs", "config.json"), nil
}

func loadConfig(filename string, allowMissing bool) (Config, error) {
	file, err := os.Open(filename)
	if os.IsNotExist(err) && allowMissing {
		return Config{}, nil
	}
	if err != nil {
		return Config{}, err
	}
	defer file.Close()
	var config Config
	decoder := json.NewDecoder(file)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&config); err != nil {
		return Config{}, fmt.Errorf("config %s: %w", filename, err)
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return Config{}, fmt.Errorf("config %s must contain one JSON object", filename)
	}
	return config, nil
}

func saveConfig(filename string, config Config) error {
	return saveJSON(filename, config)
}

func saveJSON(filename string, config any) error {
	dir := filepath.Dir(filename)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	file, err := os.CreateTemp(dir, ".config-*")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	content, err := json.MarshalIndent(config, "", "  ")
	if err == nil {
		_, err = file.Write(append(content, '\n'))
	}
	if err == nil {
		err = file.Sync()
	}
	closeErr := file.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	return os.Rename(file.Name(), filename)
}

func configRelative(filename, value string) string {
	if value == "" || filepath.IsAbs(value) {
		return value
	}
	return filepath.Join(filepath.Dir(filename), value)
}

// BranchSlug maps a full branch name to a lowercase, ref-safe workspace name.
func BranchSlug(branch string) string {
	branch = strings.TrimPrefix(branch, "refs/heads/")
	var slug strings.Builder
	dash := false
	for _, c := range strings.ToLower(branch) {
		if c >= 'a' && c <= 'z' || c >= '0' && c <= '9' {
			if dash && slug.Len() > 0 {
				slug.WriteByte('-')
			}
			slug.WriteRune(c)
			dash = false
		} else {
			dash = true
		}
	}
	return slug.String()
}

// InferWorkspace uses the current repository, or a common branch across explicit
// repository memberships when invoked from a non-repository workspace root.
func InferWorkspace(ctx context.Context, cwd, root string, repositories []string) (string, error) {
	branchAt := func(dir string) (string, error) {
		t := transaction{dir: dir, env: gitEnvironment()}
		out, err := t.git(ctx, nil, "symbolic-ref", "--quiet", "HEAD")
		return strings.TrimSpace(string(out)), err
	}
	branch, err := branchAt(cwd)
	if err != nil {
		if exitCode(err, 1) || len(repositories) == 0 {
			return "", invalid("cannot infer a workspace branch (detached HEAD or outside a repository); pass --workspace")
		}
		for _, repository := range repositories {
			if !filepath.IsAbs(repository) {
				repository = filepath.Join(root, repository)
			}
			observed, err := branchAt(repository)
			if err != nil {
				return "", invalid("cannot infer a branch from selected repositories; pass --workspace")
			}
			if branch != "" && observed != branch {
				return "", invalid("selected repositories have different branches; pass --workspace")
			}
			branch = observed
		}
	}
	slug := BranchSlug(branch)
	if slug == "" || len(slug) > 200 {
		return "", invalid("branch does not produce a supported workspace slug; pass --workspace")
	}
	return slug, nil
}
