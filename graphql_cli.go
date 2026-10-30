package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/urfave/cli/v2"
	gqlcli "github.com/wricardo/gqlcli/pkg"
)

func isGraphQLCommand(name string) bool {
	switch name {
	case "query", "q", "mutation", "m", "script", "js", "describe", "d", "types", "queries", "mutations":
		return true
	}
	return false
}

func runGraphQL(args []string, out io.Writer, addr, password string) error {
	exec := gqlcli.NewInlineExecutor(newGraphQLSchema(addr, password))
	set := gqlcli.NewInlineCommandSet(exec)
	var commands []*cli.Command
	for _, cmd := range set.Commands() {
		switch cmd.Name {
		case "query", "mutation":
			kind := cmd.Name
			cmd.Action = func(c *cli.Context) error { return runGraphQLOperation(c, exec, out, kind) }
			commands = append(commands, cmd)
		case "describe":
			cmd.Action = func(c *cli.Context) error {
				if c.NArg() != 1 {
					return fmt.Errorf("TYPE_NAME argument is required")
				}
				if c.Int("depth") < 0 {
					return fmt.Errorf("--depth must be >= 0")
				}
				text, err := gqlcli.NewDescriber(exec).DescribeWithDepth(c.Context, c.Args().First(), c.Bool("args"), c.Bool("desc"), c.Int("depth"))
				if err != nil {
					return err
				}
				_, err = io.WriteString(out, text)
				return err
			}
			commands = append(commands, cmd)
		case "types":
			cmd.Action = func(c *cli.Context) error { return listGraphQLTypes(c, exec, out) }
			commands = append(commands, cmd)
		}
	}
	for _, root := range []struct{ name, typeName string }{{"queries", "Query"}, {"mutations", "Mutation"}} {
		root := root
		commands = append(commands, &cli.Command{Name: root.name, Usage: "List GraphQL " + root.name, Flags: []cli.Flag{
			&cli.StringFlag{Name: "filter", Aliases: []string{"f"}},
			&cli.BoolFlag{Name: "args", Aliases: []string{"a"}},
			&cli.BoolFlag{Name: "desc", Aliases: []string{"descriptions"}},
			&cli.IntFlag{Name: "depth"},
		}, Action: func(c *cli.Context) error {
			if c.Int("depth") < 0 {
				return fmt.Errorf("--depth must be >= 0")
			}
			text, err := gqlcli.NewDescriber(exec).DescribeWithOptions(c.Context, root.typeName, gqlcli.DescribeOptions{FieldFilter: c.String("filter"), ShowArgs: c.Bool("args"), ShowDescriptions: c.Bool("desc"), Depth: c.Int("depth")})
			if err != nil {
				return err
			}
			_, err = io.WriteString(out, text)
			return err
		}})
	}
	commands = append(commands, scriptCommand(exec, out))
	app := &cli.App{Name: "factorio-ai-player", Writer: out, ErrWriter: out, Commands: commands}
	// urfave/cli stops parsing flags at the first positional argument. Move a
	// leading document/type behind flags so the usual gqlcli ordering works.
	argv := append([]string{"factorio-ai-player"}, args...)
	if len(argv) > 3 && !strings.HasPrefix(argv[2], "-") {
		first := argv[2]
		argv = append(append(argv[:2:2], argv[3:]...), first)
	}
	return app.RunContext(context.Background(), argv)
}

func runGraphQLOperation(c *cli.Context, exec *gqlcli.InlineExecutor, out io.Writer, kind string) error {
	var document string
	switch {
	case c.String("file") != "":
		b, err := os.ReadFile(c.String("file"))
		if err != nil {
			return fmt.Errorf("failed to read file: %w", err)
		}
		document = string(b)
	case c.String("query") != "":
		document = c.String("query")
	case c.NArg() == 1:
		document = c.Args().First()
	default:
		return fmt.Errorf("operation required: pass as argument, --query, or --file")
	}
	if err := gqlcli.RequireOperationKind(document, kind); err != nil {
		return err
	}
	var vars map[string]interface{}
	var rawVars []byte
	if path := c.String("var-file"); path != "" {
		var err error
		rawVars, err = os.ReadFile(path)
		if err != nil {
			return fmt.Errorf("failed to read variables file: %w", err)
		}
	} else if c.String("variables") != "" {
		rawVars = []byte(c.String("variables"))
	}
	if len(rawVars) > 0 {
		if err := json.Unmarshal(rawVars, &vars); err != nil {
			return fmt.Errorf("invalid variables JSON: %w", err)
		}
	}
	raw, err := exec.Execute(c.Context, document, vars)
	if err != nil {
		return err
	}
	var result map[string]interface{}
	if err := json.Unmarshal(raw, &result); err != nil {
		return err
	}
	if errs, ok := result["errors"].([]interface{}); ok && len(errs) > 0 {
		messages := make([]string, 0, len(errs))
		for _, entry := range errs {
			if e, ok := entry.(map[string]interface{}); ok {
				messages = append(messages, fmt.Sprint(e["message"]))
			}
		}
		return fmt.Errorf("GraphQL: %s", strings.Join(messages, "; "))
	}
	formatter, err := gqlcli.NewFormatterRegistry().Get(c.String("format"))
	if err != nil {
		return err
	}
	text, err := formatter.Format(result)
	if err != nil {
		return err
	}
	if path := c.String("output"); path != "" {
		return os.WriteFile(path, []byte(text), 0644)
	}
	_, err = fmt.Fprintln(out, text)
	return err
}

func listGraphQLTypes(c *cli.Context, exec *gqlcli.InlineExecutor, out io.Writer) error {
	raw, err := exec.Execute(c.Context, `{ __schema { types { name kind } } }`, nil)
	if err != nil {
		return err
	}
	var result struct {
		Data struct {
			Schema struct {
				Types []struct {
					Name string `json:"name"`
					Kind string `json:"kind"`
				} `json:"types"`
			} `json:"__schema"`
		} `json:"data"`
		Errors []struct {
			Message string `json:"message"`
		} `json:"errors"`
	}
	if err := json.Unmarshal(raw, &result); err != nil {
		return err
	}
	if len(result.Errors) > 0 {
		return fmt.Errorf("GraphQL: %s", result.Errors[0].Message)
	}
	labels := map[string]string{"OBJECT": "Types", "INTERFACE": "Interfaces", "UNION": "Unions", "ENUM": "Enums", "INPUT_OBJECT": "Inputs", "SCALAR": "Scalars"}
	for _, kind := range []string{"OBJECT", "INTERFACE", "UNION", "ENUM", "INPUT_OBJECT", "SCALAR"} {
		printed := false
		for _, typ := range result.Data.Schema.Types {
			if typ.Kind != kind || (!c.Bool("builtin") && strings.HasPrefix(typ.Name, "__")) || !strings.Contains(strings.ToLower(typ.Name), strings.ToLower(c.String("filter"))) {
				continue
			}
			if !printed {
				if _, err := fmt.Fprintf(out, "%s:\n", labels[kind]); err != nil {
					return err
				}
				printed = true
			}
			if _, err := fmt.Fprintf(out, "  %s\n", typ.Name); err != nil {
				return err
			}
		}
	}
	return nil
}
