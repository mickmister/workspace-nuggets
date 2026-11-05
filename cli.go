package nuggets

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const usage = `Usage:
  nugs workspace inspect --remote URL --workspace ID
  nugs documents list --remote URL --workspace ID
  nugs documents read --remote URL --workspace ID --name NAME
  nugs documents upsert --remote URL --workspace ID --name NAME [--expected VERSION] [--file FILE|-]
  nugs mcp --remote URL [--cache DIR]

All document commands emit JSON. Omit --expected (or use an empty value) for
create-only; pass the observed version for an update. Upsert reads stdin by
default. --cache selects storage for retained failed publication intents.
`

func RunCLI(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer) int {
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
	cache := flags.String("cache", "", "local transaction cache")
	workspace := flags.String("workspace", "", "stable workspace identifier")
	name := flags.String("name", "", "relative document name")
	expected := flags.String("expected", "", "observed version; empty means create only")
	file := flags.String("file", "-", "content file or - for stdin")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	if flags.NArg() != 0 || *remote == "" {
		fmt.Fprintln(stderr, "an explicit --remote and no positional arguments are required")
		return 2
	}
	service := Service{Remote: *remote, CacheDir: *cache}
	if operation == "mcp" {
		if err := NewMCPServer(service).Run(ctx, &mcp.StdioTransport{}); err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		return 0
	}
	req := Request{Operation: operation, Workspace: *workspace, Name: *name, ExpectedVersion: *expected}
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
