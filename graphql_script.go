package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/urfave/cli/v2"
	gqlcli "github.com/wricardo/gqlcli/pkg"
)

// scriptCommand runs gqlcli JavaScript workflows in-process against the same
// schema the query and mutation commands use.
func scriptCommand(exec *gqlcli.InlineExecutor, out io.Writer) *cli.Command {
	return &cli.Command{
		Name:    "script",
		Aliases: []string{"js"},
		Usage:   "Run a JavaScript workflow against the GraphQL schema",
		Description: "Execute JavaScript that orchestrates GraphQL queries and mutations with loops,\n" +
			"conditionals, and gql.each() concurrency. Scripts run in-process, so mutations\n" +
			"carry the same risks as the mutation command.\n\n" +
			"Script signature:\n" +
			"  async function run(gql, input) { ... }\n\n" +
			"Helpers:\n" +
			"  gql.query(query, variables?, operationName?)\n" +
			"  gql.mutation(mutation, variables?, operationName?)\n" +
			"  gql.request({ type, query|mutation, variables, operationName })\n" +
			"  gql.each(items, worker, { concurrency?, stopOnError?, onError? })",
		Flags: []cli.Flag{
			&cli.StringFlag{Name: "file", Aliases: []string{"f"}, Usage: "Path to JavaScript file"},
			&cli.StringFlag{Name: "source", Usage: "Inline JavaScript source code"},
			&cli.StringFlag{Name: "op", Usage: "Saved script name from .gqlcli.json scripts"},
			&cli.StringFlag{Name: "function", Value: "run", Usage: "JavaScript function name to call"},
			&cli.StringFlag{Name: "arg", Usage: "JSON object passed as the second script argument"},
			&cli.StringFlag{Name: "arg-file", Usage: "Path to JSON file passed as the second script argument"},
			&cli.StringFlag{Name: "output", Aliases: []string{"o"}, Usage: "Write script return value as JSON to a file (default: stdout)"},
		},
		Action: func(c *cli.Context) error {
			sourceName, source, function, defaults, err := resolveScriptSource(c)
			if err != nil {
				return err
			}
			input, err := readScriptInput(c)
			if err != nil {
				return err
			}
			input = mergeScriptInput(defaults, input)

			runner := gqlcli.NewScriptRunner(
				gqlcli.NewInlineClient(exec),
				gqlcli.WithStdout(out),
				gqlcli.WithStderr(out),
			)
			result, err := runner.RunSource(c.Context, sourceName, source, function, input)
			if err != nil {
				return err
			}
			if result == nil {
				return nil
			}
			raw, err := json.MarshalIndent(result, "", "  ")
			if err != nil {
				return fmt.Errorf("failed to serialize script result: %w", err)
			}
			if path := c.String("output"); path != "" {
				return os.WriteFile(path, raw, 0644)
			}
			_, err = fmt.Fprintln(out, string(raw))
			return err
		},
	}
}

func resolveScriptSource(c *cli.Context) (sourceName, source, function string, defaults map[string]interface{}, err error) {
	file := c.String("file")
	inlineSource := c.String("source")
	opName := c.String("op")

	set := 0
	for _, value := range []string{file, inlineSource, opName} {
		if value != "" {
			set++
		}
	}
	if set == 0 {
		return "", "", "", nil, fmt.Errorf("one of --file, --source, or --op is required")
	}
	if set > 1 {
		return "", "", "", nil, fmt.Errorf("only one of --file, --source, or --op may be set")
	}

	function = c.String("function")
	if opName != "" {
		cfg, err := gqlcli.LoadProjectConfig()
		if err != nil {
			return "", "", "", nil, err
		}
		if cfg == nil {
			return "", "", "", nil, fmt.Errorf("--op requires .gqlcli.json")
		}
		script, err := cfg.ResolveScript(opName)
		if err != nil {
			return "", "", "", nil, err
		}
		if !c.IsSet("function") {
			function = script.Function
		}
		if function == "" {
			function = "run"
		}
		return ".gqlcli.json:scripts." + opName, script.Source, function, script.Defaults, nil
	}

	if inlineSource != "" {
		if function == "" {
			function = "run"
		}
		return "--source", inlineSource, function, nil, nil
	}

	data, err := os.ReadFile(file)
	if err != nil {
		return "", "", "", nil, fmt.Errorf("failed to read script file: %w", err)
	}
	if function == "" {
		function = "run"
	}
	return file, string(data), function, nil, nil
}

func readScriptInput(c *cli.Context) (map[string]interface{}, error) {
	var input map[string]interface{}
	if file := c.String("arg-file"); file != "" {
		data, err := os.ReadFile(file)
		if err != nil {
			return nil, fmt.Errorf("failed to read arg file: %w", err)
		}
		if err := json.Unmarshal(data, &input); err != nil {
			return nil, fmt.Errorf("invalid JSON in arg file: %w", err)
		}
		return input, nil
	}
	if raw := strings.TrimSpace(c.String("arg")); raw != "" {
		if err := json.Unmarshal([]byte(raw), &input); err != nil {
			return nil, fmt.Errorf("invalid --arg JSON: %w", err)
		}
	}
	return input, nil
}

func mergeScriptInput(defaults, input map[string]interface{}) map[string]interface{} {
	if len(defaults) == 0 {
		return input
	}
	merged := make(map[string]interface{}, len(defaults)+len(input))
	for key, value := range defaults {
		merged[key] = value
	}
	for key, value := range input {
		merged[key] = value
	}
	return merged
}
