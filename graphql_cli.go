package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/urfave/cli/v2"
	gqlcli "github.com/wricardo/gqlcli/pkg"
)

func isGraphQLCommand(name string) bool {
	return false
}

func runGraphQL(args []string, out io.Writer, addr, password string) error {
	return fmt.Errorf("inline GraphQL CLI removed; use `serve` with gqlcli")
}

func runGraphQLOperation(c *cli.Context, exec graphQLExecutor, out io.Writer, kind string) error {
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
		recordGraphQLOperation("cli", kind, document, nil, "", time.Now(), nil, 0, err)
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
			err = fmt.Errorf("invalid variables JSON: %w", err)
			recordGraphQLOperation("cli", kind, document, nil, "", time.Now(), nil, 0, err)
			return err
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
		// A nested failure keeps every sibling that did resolve, so print the
		// partial data before failing; dropping it turns one bad row into a
		// lost page.
		if partial, ok := graphqlPartialData(result); ok {
			if err := writeGraphQLResult(c, out, partial); err != nil {
				return err
			}
		}
		return fmt.Errorf("GraphQL: %s", graphqlErrorSummary(errs))
	}
	return writeGraphQLResult(c, out, result)
}

// graphqlPartialData returns the data document to print when a response also
// carries errors. A null data field means nothing resolved, so there is nothing
// to print and the caller only reports the errors.
func graphqlPartialData(result map[string]interface{}) (map[string]interface{}, bool) {
	data, ok := result["data"]
	if !ok || data == nil {
		return nil, false
	}
	return map[string]interface{}{"data": data}, true
}

// writeGraphQLResult formats and writes a response document as requested.
//
// The json formats are rendered here instead of through the shared formatter
// registry because that formatter drops null values: a GraphQL response must
// carry every field the client selected, so a nullable field that resolved to
// null has to appear. Without this, clients cannot tell an absent field from a
// null one and cannot verify that, for example, ghost-only fields stay null on
// a real entity. The other formats (table, compact, toon, llm) keep the shared
// behaviour.
func writeGraphQLResult(c *cli.Context, out io.Writer, result map[string]interface{}) error {
	var text string
	switch c.String("format") {
	case "json", "json-pretty":
		var encoded []byte
		var err error
		if c.String("format") == "json-pretty" {
			encoded, err = json.MarshalIndent(result, "", "  ")
		} else {
			encoded, err = json.Marshal(result)
		}
		if err != nil {
			return fmt.Errorf("failed to marshal JSON: %w", err)
		}
		text = string(encoded)
	default:
		formatter, err := gqlcli.NewFormatterRegistry().Get(c.String("format"))
		if err != nil {
			return err
		}
		text, err = formatter.Format(result)
		if err != nil {
			return err
		}
	}
	if path := c.String("output"); path != "" {
		return os.WriteFile(path, []byte(text), 0644)
	}
	_, err := fmt.Fprintln(out, text)
	return err
}

// graphqlErrorSummary renders one message per error, each prefixed with its
// response path so a failure names the field or list row that failed instead of
// an unattributed message.
func graphqlErrorSummary(errs []interface{}) string {
	messages := make([]string, 0, len(errs))
	for _, entry := range errs {
		e, ok := entry.(map[string]interface{})
		if !ok {
			messages = append(messages, fmt.Sprint(entry))
			continue
		}
		message := fmt.Sprint(e["message"])
		if path := graphqlErrorPath(e["path"]); path != "" {
			message = path + ": " + message
		}
		messages = append(messages, message)
	}
	return strings.Join(messages, "; ")
}

// graphqlErrorPath renders an error path array as GraphQL-style segments.
func graphqlErrorPath(value interface{}) string {
	items, ok := value.([]interface{})
	if !ok || len(items) == 0 {
		return ""
	}
	parts := make([]string, 0, len(items))
	for _, item := range items {
		switch v := item.(type) {
		case string:
			parts = append(parts, v)
		case float64:
			parts = append(parts, strconv.Itoa(int(v)))
		default:
			parts = append(parts, fmt.Sprint(v))
		}
	}
	return strings.Join(parts, ".")
}

func listGraphQLTypes(c *cli.Context, exec graphQLExecutor, out io.Writer) error {
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
