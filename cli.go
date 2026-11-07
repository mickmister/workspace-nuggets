package nuggets

import (
	"context"
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
  nugs workspace init [--root DIR] [--workspace HANDLE]
  nugs workspace attach --root DIR [--workspace-ref REF | --engagement NAME --remote URL --workspace HANDLE]
  nugs workspace restore --root DIR --data-dir DIR --file HANDOFF.json --remote URL
  nugs workspace context
  nugs workspace list
  nugs storage bootstrap
  nugs workspace inspect
  nugs workspace provision --root DIR --repo RELPATH [--repo RELPATH] [--expected VERSION]
  nugs beads inspect [--root DIR]
  nugs beads configure [--root DIR]
  nugs beads publish [--root DIR]
  nugs beads restore --root DIR --data-dir DIR --collection-id ID --dolt-commit HASH
  nugs beads catch-up [--root DIR] --dolt-commit HASH
  nugs documents list
  nugs documents read --name NAME
  nugs documents upsert --name NAME [--expected VERSION] [--file FILE|-]
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
	dataDir := flags.String("data-dir", "", "new persistent collection directory for native restoration")
	collectionID := flags.String("collection-id", "", "expected collection identity from a native publication receipt")
	doltCommit := flags.String("dolt-commit", "", "expected native Dolt checkpoint from a publication receipt")
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
	config, err := loadConfig(filename, !seen["config"] || operation == "config.set")
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 2
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
	service := Service{RegistryPath: registryPath, ContextDir: cwd, WorkspaceReference: *workspaceRef}
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
	req := Request{Operation: operation, Workspace: *workspace, WorkspaceReference: *workspaceRef, Name: *name, ExpectedVersion: *expected, Root: *root, Repositories: repositories, DataDir: *dataDir, CollectionID: *collectionID, DoltCommit: *doltCommit}
	if operation == "documents.upsert" {
		var err error
		if *file == "-" {
			req.Content, err = io.ReadAll(stdin)
		} else {
			req.Content, err = os.ReadFile(*file)
		}
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
	}
	response := service.Execute(ctx, req)
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
