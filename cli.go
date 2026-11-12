package nuggets

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const usage = `Usage:
  nugs config set --remote URL [--cache DIR]
  nugs config show
  nugs config target --target NAME --file PROFILE.json
  nugs types list
  nugs media bind --target NAME
  nugs media import --target NAME --name ID --file FILE [--filename NAME] [--category screenshot] [--source URI] [--expected REVISION]
  nugs media list --target NAME [--limit N] [--token TOKEN]
  nugs media read --target NAME --name ID [--snapshot HASH] [--file NEW-OUTPUT-FILE]
  nugs media verify --target NAME --name ID [--snapshot HASH]
  nugs config message-target --target NAME --file PROFILE.json
  nugs messages bind --target NAME
  nugs messages scan|sync [--target NAME] [--database FILE] [--native-version VERSION] [--session ID] [--repo MEMBER ...] [--alias OLD-DIR ...] [--limit N]
  nugs messages import [--target NAME] --file NORMALIZED-BATCH.json
  nugs messages sessions|read|verify [--target NAME] [--session ID] [--role user|assistant] [--name MESSAGE-ID] [--snapshot HASH]
  nugs messages prepare|catch-up [--target NAME] --reader NAME [--limit N]
  nugs messages read-page|ack [--target NAME] --reader NAME --token TOKEN
  nugs messages reader [--target NAME] --reader NAME
  nugs workspace init [--root DIR] [--workspace HANDLE]
  nugs workspace attach --root DIR [--workspace-ref REF | --engagement NAME --remote URL --workspace HANDLE]
  nugs workspace restore --root DIR --data-dir DIR --file HANDOFF.json --remote URL
  nugs workspace context
  nugs workspace list
  nugs storage bootstrap
  nugs workspace inspect
  nugs workspace provision [--root DIR] [--repo RELPATH ...] [--purpose TEXT] [--expected VERSION]
  nugs beads inspect [--root DIR]
  nugs beads configure [--root DIR]
  nugs beads publish [--root DIR]
  nugs beads sync [--root DIR]
  nugs beads restore --root DIR --data-dir DIR --collection-id ID --dolt-commit HASH
  nugs beads catch-up [--root DIR] --dolt-commit HASH
  nugs documents list
  nugs documents read --name NAME
  nugs documents upsert --name NAME [--expected VERSION] [--file FILE|-]
  nugs runs create --run ID --purpose TEXT [--task REFERENCE]
  nugs runs inspect --run ID [--snapshot COMMIT]
  nugs runs read --run ID --name NAME [--snapshot COMMIT]
  nugs runs write --run ID --name NAME [--expected VERSION] [--file FILE|-]
  nugs mcp

Onboarding policy lives in the machine's nugs/config.json under its user config
directory; --config FILE selects setup policy without changing existing bindings.
Ordinary operations use the persistent directory attachment, independent of
branch changes and machine defaults. --workspace-ref selects a registered
workspace in the same engagement. Setup alone may infer a readable branch handle.
config set --engagement NAME --remote URL sets onboarding policy, not a rebind.
All document commands emit JSON. Omit --expected (or use an empty value) for
create-only; pass the observed version for an update. Upsert reads stdin by
default. Bound caches retain failed publication intents per engagement/workspace.

Agent workflow:
` + agentArtifactInstructions + "\n"

type repositoryFlags []string

func (r *repositoryFlags) String() string         { return fmt.Sprint([]string(*r)) }
func (r *repositoryFlags) Set(value string) error { *r = append(*r, value); return nil }

func RunCLI(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	if len(args) == 1 && args[0] == "--version" {
		fmt.Fprintln(stdout, "nugs "+Version)
		return 0
	}
	if len(args) == 0 || args[0] == "help" || args[0] == "--help" {
		fmt.Fprint(stdout, usage)
		return 0
	}
	operation := args[0]
	if operation != "mcp" {
		if len(args) < 2 {
			fmt.Fprint(stderr, usage)
			return 2
		}
		operation += "." + args[1]
		args = args[2:]
	} else {
		args = args[1:]
	}
	flags := flag.NewFlagSet("nugs "+operation, flag.ContinueOnError)
	flags.SetOutput(stderr)
	remote := flags.String("remote", "", "Git URL or repository path")
	configFile := flags.String("config", "", "config file (default: user config directory/nugs/config.json)")
	cache := flags.String("cache", "", "local transaction cache")
	workspace := flags.String("workspace", "", "readable workspace handle; setup can infer it once from a branch")
	workspaceRef := flags.String("workspace-ref", "", "stable registered workspace reference")
	engagement := flags.String("engagement", "", "engagement policy for explicit workspace setup")
	name := flags.String("name", "", "relative document name")
	expected := flags.String("expected", "", "observed version; empty means create only")
	file := flags.String("file", "-", "content file or - for stdin")
	root := flags.String("root", ".", "workspace root for repository memberships")
	purpose := flags.String("purpose", "", "why the workspace exists; required when no --repo is supplied")
	dataDir := flags.String("data-dir", "", "new persistent collection directory for native restoration")
	collectionID := flags.String("collection-id", "", "expected collection identity from a native publication receipt")
	doltCommit := flags.String("dolt-commit", "", "expected native Dolt checkpoint from a publication receipt")
	runID := flags.String("run", "", "stable workflow run identity")
	snapshot := flags.String("snapshot", "", "immutable document commit for pinned document or workflow run reads")
	task := flags.String("task", "", "originating task reference for a workflow run")
	mediaFilename := flags.String("filename", "", "original media filename")
	category := flags.String("category", "", "media category, for example screenshot")
	source := flags.String("source", "", "media provenance/source reference")
	target := flags.String("target", "", "explicit named message target")
	otherTarget := flags.String("other-target", "", "second bound message target for explicit semantic comparison")
	database := flags.String("database", defaultOpenCodeDatabase(), "read-only native OpenCode SQLite database")
	nativeVersion := flags.String("native-version", "", "observed OpenCode version; omitted invokes opencode --version")
	vkDatabase := flags.String("vk-database", "", "optional read-only VK SQLite association verification")
	vkWorkspace := flags.String("vk-workspace", "", "explicit VK workspace UUID for association verification")
	session := flags.String("session", "", "native session ID or canonical message session ID")
	role := flags.String("role", "", "authored-message role filter")
	reader := flags.String("reader", "", "stable logical reader, independent of native session/CI run")
	token := flags.String("token", "", "opaque issued page token")
	limit := flags.Int("limit", 100, "message page size or native scan session limit; sync --limit 0 scans all matching sessions")
	var aliases repositoryFlags
	flags.Var(&aliases, "alias", "explicit historical native directory alias; repeatable")
	var repositories repositoryFlags
	flags.Var(&repositories, "repo", "repository path relative to --root; repeat for all memberships")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	if flags.NArg() != 0 {
		fmt.Fprintln(stderr, "no positional arguments are allowed after the operation")
		return 2
	}
	seen := map[string]bool{}
	flags.Visit(func(f *flag.Flag) { seen[f.Name] = true })
	if strings.HasPrefix(operation, "beads.") && !seen["root"] {
		*root = "" // Discover the workspace redirect, never a repository database.
	}
	if operation == "workspace.provision" && !seen["root"] {
		*root = ""
	}
	if *configFile == "" {
		var err error
		*configFile, err = ConfigPath()
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 2
		}
	}
	filename, err := filepath.Abs(*configFile)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	config, err := loadConfig(filename, !seen["config"] || operation == "config.set" || operation == "config.message-target" || operation == "config.target")
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	if operation == "config.message-target" || operation == "config.target" {
		if err := validateMessageLabel(*target); err != nil {
			fmt.Fprintln(stderr, err)
			return 2
		}
		var content []byte
		if *file == "-" {
			content, err = io.ReadAll(io.LimitReader(stdin, 65537))
		} else {
			content, err = os.ReadFile(*file)
		}
		var profile MessageTarget
		if len(content) > 65536 {
			fmt.Fprintln(stderr, "target profile exceeds 64 KiB")
			return 2
		}
		if err == nil {
			decoder := json.NewDecoder(strings.NewReader(string(content)))
			decoder.DisallowUnknownFields()
			err = decoder.Decode(&profile)
			if err == nil {
				if trailing := decoder.Decode(new(any)); trailing != io.EOF {
					err = invalid("target profile must contain one JSON object")
				}
			}
		}
		if err == nil {
			err = profile.validateWrite()
		}
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 2
		}
		if profile.Remote != "" && !strings.Contains(profile.Remote, ":") {
			profile.Remote, err = filepath.Abs(profile.Remote)
		}
		if profile.CredentialsFile != "" {
			profile.CredentialsFile, err = filepath.Abs(profile.CredentialsFile)
		}
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 2
		}
		if config.MessageTargets == nil {
			config.MessageTargets = map[string]MessageTarget{}
		}
		config.MessageTargets[*target] = profile
		if err := saveConfig(filename, config); err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		return 0
	}
	if operation == "config.set" || operation == "config.show" {
		if operation == "config.set" {
			if !seen["remote"] && !seen["cache"] && !seen["engagement"] {
				fmt.Fprintln(stderr, "config set requires --remote, --cache or --engagement")
				return 2
			}
			if seen["engagement"] {
				config.Engagement = *engagement
			}
			if seen["remote"] {
				config.Remote = *remote
				if config.Remote != "" && !strings.Contains(config.Remote, ":") {
					config.Remote, err = filepath.Abs(config.Remote)
				}
			}
			if err == nil && seen["cache"] {
				config.CacheDir = *cache
				if config.CacheDir != "" {
					config.CacheDir, err = filepath.Abs(config.CacheDir)
				}
			}
			if err == nil {
				err = saveConfig(filename, config)
			}
			if err != nil {
				fmt.Fprintln(stderr, err)
				return 1
			}
		}
		if err := json.NewEncoder(stdout).Encode(struct {
			Path   string `json:"path"`
			Config Config `json:"config"`
		}{filename, config}); err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		return 0
	}
	registryPath, err := RegistryPath()
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	if operation == "workspace.restore" {
		if !seen["remote"] || *remote == "" || seen["workspace"] || seen["workspace-ref"] || seen["engagement"] || seen["collection-id"] || seen["dolt-commit"] {
			fmt.Fprintln(stderr, "workspace restore requires explicit setup --remote and a handoff file, without conflicting identity selectors")
			return 2
		}
		var content []byte
		if *file == "-" {
			content, err = io.ReadAll(io.LimitReader(stdin, 8193))
		} else {
			content, err = os.ReadFile(*file)
		}
		var h *WorkspaceHandoff
		if err == nil {
			h, err = ParseWorkspaceHandoff(content)
		}
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 2
		}
		response := RestoreWorkspaceContext(ctx, registryPath, *root, *dataDir, *remote, *h)
		if err := json.NewEncoder(stdout).Encode(response); err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		if response.Error != nil {
			return 1
		}
		return 0
	}
	if operation == "workspace.init" || operation == "workspace.attach" {
		setup := BindingSetup{Root: *root, Reference: *workspaceRef, Engagement: *engagement, Workspace: *workspace, Remote: *remote}
		if setup.Remote != "" && !strings.Contains(setup.Remote, ":") {
			setup.Remote, err = filepath.Abs(setup.Remote)
		}
		var result WorkspaceContext
		if err == nil {
			result, err = setupFromPolicy(ctx, registryPath, filename, operation, setup, config, repositories)
		}
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		if err := json.NewEncoder(stdout).Encode(result); err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		return 0
	}
	cwd, err := os.Getwd()
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	registry, err := readRegistry(registryPath)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	binding, _, contextErr := registry.resolve(cwd, *workspaceRef, *workspace)
	service := Service{RegistryPath: registryPath, ContextDir: cwd, WorkspaceReference: *workspaceRef, MessageTargets: config.MessageTargets}
	if contextErr != nil {
		// Retain the explicit transport adapter used by existing trusted CI
		// handoffs. Mutable machine defaults and branch inference never enter it.
		opErr, unbound := contextErr.(*OperationError)
		_, _, attachedErr := registry.resolve(cwd, "", "")
		if !unbound || opErr.Code != "workspace_unbound" || attachedErr == nil || !seen["remote"] || !seen["workspace"] || *workspaceRef != "" {
			fmt.Fprintln(stderr, contextErr)
			return 2
		}
		service = Service{Remote: *remote, CacheDir: *cache, DefaultWorkspace: *workspace}
	} else if seen["remote"] && *remote != binding.Remote || seen["cache"] && *cache != binding.CacheDir {
		fmt.Fprintln(stderr, "explicit transport/cache conflicts with persistent workspace binding")
		return 2
	}
	if operation == "mcp" {
		if err := NewMCPServer(service).Run(ctx, &mcp.StdioTransport{}); err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		return 0
	}
	req := Request{Operation: operation, Workspace: *workspace, WorkspaceReference: *workspaceRef, Name: *name, ExpectedVersion: *expected, Root: *root, Repositories: repositories, Purpose: *purpose, DataDir: *dataDir, CollectionID: *collectionID, DoltCommit: *doltCommit, RunID: *runID, Snapshot: *snapshot, Task: *task}
	req.Target, req.Database, req.NativeVersion, req.Session, req.Role, req.Reader, req.Token, req.Limit, req.Aliases = *target, *database, *nativeVersion, *session, *role, *reader, *token, *limit, aliases
	req.VKDatabase, req.VKWorkspace = *vkDatabase, *vkWorkspace
	req.OtherTarget = *otherTarget
	req.Category, req.Source = *category, *source
	if operation == "media.import" {
		req.Purpose = *mediaFilename
	}
	if operation == "documents.upsert" || operation == "runs.write" || operation == "messages.import" || operation == "media.import" {
		var err error
		if *file == "-" {
			input := stdin
			if operation == "media.import" {
				input = io.LimitReader(stdin, mediaSizeLimit+1)
			}
			req.Content, err = io.ReadAll(input)
		} else {
			if operation == "media.import" {
				var f *os.File
				f, err = os.Open(*file)
				if err == nil {
					req.Content, err = io.ReadAll(io.LimitReader(f, mediaSizeLimit+1))
					f.Close()
				}
				if req.Purpose == "" {
					req.Purpose = filepath.Base(*file)
				}
			} else {
				req.Content, err = os.ReadFile(*file)
			}
		}
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
	}
	response := service.Execute(ctx, req)
	if operation == "media.read" && response.Error == nil && response.Media != nil && seen["file"] && *file != "-" {
		content, err := base64.StdEncoding.DecodeString(response.Media.ContentBase64)
		if err == nil {
			var output *os.File
			output, err = os.OpenFile(*file, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
			if err == nil {
				_, err = output.Write(content)
				closeErr := output.Close()
				if err == nil {
					err = closeErr
				}
			}
		}
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		response.Media.ContentBase64 = ""
		response.Media.OutputFile = *file
	}
	if err := json.NewEncoder(stdout).Encode(response); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	if response.Error != nil {
		if response.Error.Code == "conflict" {
			return 3
		}
		return 1
	}
	return 0
}
