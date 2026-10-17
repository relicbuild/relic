package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/spf13/cobra"
	"go.yaml.in/yaml/v3"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"

	"relic/internal/platform/terminal"
)

func outputFlag(cmd *cobra.Command, output *string) {
	cmd.Flags().StringVarP(output, "output", "o", "text", "output format: text, json or yaml")
	_ = cmd.RegisterFlagCompletionFunc(
		"output",
		cobra.FixedCompletions(
			[]string{
				"text",
				"json",
				"yaml",
			},
			cobra.ShellCompDirectiveNoFileComp,
		),
	)
}

func printMessage(w io.Writer, output string, v any, text func(io.Writer) error) error {
	switch output {
	case "text", "":
		return text(w)
	case "json", "yaml":
		var (
			b   []byte
			err error
		)

		if m, ok := v.(proto.Message); ok {
			b, err = protojson.Marshal(m)
		} else {
			b, err = json.Marshal(v)
		}

		if err != nil {
			return err
		}

		if output == "json" {
			var out bytes.Buffer
			if err := json.Indent(&out, b, "", "  "); err != nil {
				return err
			}

			out.WriteByte('\n')
			_, err = w.Write(out.Bytes())

			return err
		}

		var doc any
		if err := yaml.Unmarshal(b, &doc); err != nil {
			return err
		}

		enc := yaml.NewEncoder(w)
		enc.SetIndent(2)

		if err := enc.Encode(doc); err != nil {
			return err
		}

		return enc.Close()
	default:
		return fmt.Errorf("unknown output %q: use text, json or yaml", output)
	}
}

func printCompleted(w io.Writer, completed terminal.Table, failure error) error {
	if len(completed.Rows) == 0 {
		return failure
	}

	return errors.Join(completed.Print(w), failure)
}

func localTime(timestamp *timestamppb.Timestamp, layout string) string {
	if timestamp == nil {
		return ""
	}

	return timestamp.AsTime().Local().Format(layout)
}

func tableCell(text string) string {
	return strings.Join(strings.Fields(text), " ")
}
