// Port of tart's Formatter/Format.swift. Swift's TextTable + Mirror
// reflection becomes a small reflect-based plain-table renderer; the JSON
// branch matches JSONEncoder's .prettyPrinted output.
//go:build darwin

package command

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
)

// Format mirrors tart's Format enum.
type Format string

const (
	FormatText Format = "text"
	FormatJSON Format = "json"
)

// ParseFormat ports the ExpressibleByArgument conformance.
func ParseFormat(argument string) (Format, bool) {
	switch Format(argument) {
	case FormatText, FormatJSON:
		return Format(argument), true
	default:
		return "", false
	}
}

// FormatAllValueStrings mirrors Format.allValueStrings.
func FormatAllValueStrings() []string {
	return []string{string(FormatText), string(FormatJSON)}
}

// RenderSingle ports Format.renderSingle(_:).
func (f Format) RenderSingle(data any) string {
	if f == FormatJSON {
		return renderJSON(data)
	}
	return f.RenderList([]any{data})
}

// RenderList ports Format.renderList(_:).
func (f Format) RenderList(data []any) string {
	if f == FormatJSON {
		return renderJSON(data)
	}

	if len(data) == 0 {
		return ""
	}

	headers, rows := tableColumns(data)
	return renderPlainTable(headers, rows)
}

func renderJSON(data any) string {
	out, err := json.MarshalIndent(data, "", "  ")
	if err != nil {
		panic(err) // Swift uses try!
	}
	return string(out)
}

// tableColumns mirrors the Mirror-based column extraction, including the
// exception that deprecates the "Running" field from text output (it stays
// available in JSON for backwards-compatibility).
func tableColumns(data []any) ([]string, [][]string) {
	var headers []string
	rows := make([][]string, 0, len(data))

	for i, item := range data {
		value := reflect.ValueOf(item)
		for value.Kind() == reflect.Pointer {
			value = value.Elem()
		}

		var row []string
		for fieldIndex := 0; fieldIndex < value.NumField(); fieldIndex++ {
			field := value.Type().Field(fieldIndex)
			if !field.IsExported() || field.Name == "Running" {
				continue
			}
			if i == 0 {
				headers = append(headers, field.Name)
			}
			row = append(row, fmt.Sprintf("%v", value.Field(fieldIndex).Interface()))
		}
		rows = append(rows, row)
	}

	return headers, rows
}

// renderPlainTable mimics TextTable's plain style: space-aligned columns.
func renderPlainTable(headers []string, rows [][]string) string {
	widths := make([]int, len(headers))
	for i, header := range headers {
		widths[i] = len(header)
	}
	for _, row := range rows {
		for i, cell := range row {
			if i < len(widths) && len(cell) > widths[i] {
				widths[i] = len(cell)
			}
		}
	}

	var sb strings.Builder
	writeRow := func(cells []string) {
		for i, cell := range cells {
			if i > 0 {
				sb.WriteString("  ")
			}
			if i == len(cells)-1 {
				sb.WriteString(cell)
			} else {
				sb.WriteString(fmt.Sprintf("%-*s", widths[i], cell))
			}
		}
		sb.WriteString("\n")
	}

	writeRow(headers)
	for _, row := range rows {
		writeRow(row)
	}

	return strings.TrimSpace(sb.String())
}
