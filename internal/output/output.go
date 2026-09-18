// Package output renders JSON-compatible values without converting numbers to float64.
package output

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"

	"go.yaml.in/yaml/v3"
)

func Valid(format string) bool {
	return format == "yaml" || format == "json" || format == "jsonl" || format == "toon"
}

func Write(w io.Writer, format string, value any) error {
	b, err := json.Marshal(value)
	if err != nil {
		return err
	}
	switch format {
	case "json":
		var pretty bytes.Buffer
		if err = json.Indent(&pretty, b, "", "  "); err != nil {
			return err
		}
		_, err = fmt.Fprintln(w, pretty.String())
	case "jsonl":
		_, err = fmt.Fprintln(w, string(b))
	case "yaml", "toon":
		d := json.NewDecoder(bytes.NewReader(b))
		d.UseNumber()
		var v any
		if err = d.Decode(&v); err != nil {
			return err
		}
		if format == "yaml" {
			e := yaml.NewEncoder(w)
			e.SetIndent(2)
			err = e.Encode(yamlNode(v, nil))
			if closeErr := e.Close(); err == nil {
				err = closeErr
			}
		} else {
			var buf strings.Builder
			if err = toon(&buf, "", v, 0, nil); err != nil {
				return err
			}
			_, err = io.WriteString(w, buf.String())
		}
	default:
		return fmt.Errorf("unsupported output format")
	}
	return err
}

// Sorting these alphabetically would open on apiCode and bury message, the line worth reading, at the
// bottom of what YAML shows by default.
var errorFieldOrder = map[string]int{
	"code": 0, "message": 1, "httpStatus": 2, "apiCode": 3, "details": 4,
}

// A nil order sorts alphabetically, which is the stable choice for a server response whose field
// order is not part of any contract.
func keys(m map[string]any, order map[string]int) []string {
	k := make([]string, 0, len(m))
	for key := range m {
		k = append(k, key)
	}
	if order == nil {
		sort.Strings(k)
		return k
	}
	sort.Slice(k, func(i, j int) bool {
		ranked, known := order[k[i]]
		other, otherKnown := order[k[j]]
		if known != otherKnown {
			return known
		}
		if known && ranked != other {
			return ranked < other
		}
		return k[i] < k[j]
	})
	return k
}

// Only the error object itself has a fixed order; details is arbitrary JSON from the server.
func orderFor(key string) map[string]int {
	if key == "error" {
		return errorFieldOrder
	}
	return nil
}

func yamlNode(v any, order map[string]int) *yaml.Node {
	n := &yaml.Node{Kind: yaml.ScalarNode}
	switch x := v.(type) {
	case nil:
		n.Tag, n.Value = "!!null", "null"
	case string:
		n.Tag, n.Value, n.Style = "!!str", x, yaml.DoubleQuotedStyle
	case bool:
		n.Tag, n.Value = "!!bool", strconv.FormatBool(x)
	case json.Number:
		n.Tag, n.Value = "!!int", x.String()
		if strings.ContainsAny(n.Value, ".eE") {
			n.Tag = "!!float"
		}
	case []any:
		n.Kind, n.Tag = yaml.SequenceNode, "!!seq"
		for _, item := range x {
			n.Content = append(n.Content, yamlNode(item, nil))
		}
	case map[string]any:
		n.Kind, n.Tag = yaml.MappingNode, "!!map"
		for _, key := range keys(x, order) {
			n.Content = append(n.Content, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: key},
				yamlNode(x[key], orderFor(key)))
		}
	}
	return n
}

func toon(b *strings.Builder, key string, v any, depth int, order map[string]int) error {
	indent := strings.Repeat("  ", depth)
	switch x := v.(type) {
	case map[string]any:
		if key != "" {
			fmt.Fprintf(b, "%s%s:\n", indent, key)
			depth++
		}
		for _, k := range keys(x, order) {
			if err := toon(b, quote(k), x[k], depth, orderFor(k)); err != nil {
				return err
			}
		}
	case []any:
		if fields := tableFields(x); len(fields) > 0 {
			names := make([]string, len(fields))
			for i, field := range fields {
				names[i] = quote(field)
			}
			fmt.Fprintf(b, "%s%s[%d]{%s}:\n", indent, key, len(x), strings.Join(names, ","))
			for _, row := range x {
				values := make([]string, len(fields))
				for i, field := range fields {
					value, err := scalar(row.(map[string]any)[field])
					if err != nil {
						return err
					}
					values[i] = value
				}
				fmt.Fprintf(b, "%s  %s\n", indent, strings.Join(values, ","))
			}
			return nil
		}
		header := fmt.Sprintf("%s%s[%d]:", indent, key, len(x))
		flat := true
		for _, item := range x {
			switch item.(type) {
			case map[string]any, []any:
				flat = false
			}
		}
		if flat {
			parts := make([]string, len(x))
			for i, item := range x {
				s, err := scalar(item)
				if err != nil {
					return err
				}
				parts[i] = s
			}
			b.WriteString(header)
			if len(parts) > 0 {
				b.WriteString(" " + strings.Join(parts, ","))
			}
			b.WriteByte('\n')
		} else {
			b.WriteString(header + "\n")
			for _, item := range x {
				switch item.(type) {
				case map[string]any:
					var child strings.Builder
					if err := toon(&child, "", item, depth+2, nil); err != nil {
						return err
					}
					lines := strings.Split(strings.TrimSuffix(child.String(), "\n"), "\n")
					if child.Len() == 0 {
						b.WriteString(indent + "  -\n")
						continue
					}
					b.WriteString(indent + "  - " + strings.TrimPrefix(lines[0], indent+"    ") + "\n")
					for _, line := range lines[1:] {
						b.WriteString(line + "\n")
					}
				case []any:
					if err := toon(b, "- ", item, depth+1, nil); err != nil {
						return err
					}
				default:
					s, err := scalar(item)
					if err != nil {
						return err
					}
					b.WriteString(indent + "  - " + s + "\n")
				}
			}
		}
	default:
		s, err := scalar(v)
		if err != nil {
			return err
		}
		if key == "" {
			fmt.Fprintf(b, "%s%s\n", indent, s)
		} else {
			fmt.Fprintf(b, "%s%s: %s\n", indent, key, s)
		}
	}
	return nil
}

func tableFields(rows []any) []string {
	if len(rows) == 0 {
		return nil
	}
	first, ok := rows[0].(map[string]any)
	if !ok || len(first) == 0 {
		return nil
	}
	fields := keys(first, nil)
	for _, row := range rows {
		obj, ok := row.(map[string]any)
		if !ok || len(obj) != len(fields) {
			return nil
		}
		for _, field := range fields {
			value, exists := obj[field]
			if !exists {
				return nil
			}
			switch value.(type) {
			case map[string]any, []any:
				return nil
			}
		}
	}
	return fields
}

func quote(s string) string {
	// TOON accepts these five escapes; JSON's control-character escapes are not portable.
	r := strings.NewReplacer("\\", "\\\\", "\"", "\\\"", "\n", "\\n", "\r", "\\r", "\t", "\\t")
	return "\"" + r.Replace(s) + "\""
}

func scalar(v any) (string, error) {
	switch x := v.(type) {
	case nil:
		return "null", nil
	case string:
		return quote(x), nil
	case bool:
		return strconv.FormatBool(x), nil
	case json.Number:
		return decimal(x.String())
	default:
		return "", fmt.Errorf("unsupported scalar")
	}
}

func decimal(s string) (string, error) {
	negative := strings.HasPrefix(s, "-")
	s = strings.TrimPrefix(s, "-")
	exponent := 0
	if i := strings.IndexAny(s, "eE"); i >= 0 {
		var err error
		exponent, err = strconv.Atoi(s[i+1:])
		if err != nil || exponent > 10000 || exponent < -10000 {
			return "", fmt.Errorf("number exponent exceeds TOON output limit")
		}
		s = s[:i]
	}
	point := len(s)
	if i := strings.IndexByte(s, '.'); i >= 0 {
		point = i
		s = s[:i] + s[i+1:]
	}
	point += exponent
	if point <= 0 {
		s = "0." + strings.Repeat("0", -point) + s
	} else if point >= len(s) {
		s += strings.Repeat("0", point-len(s))
	} else {
		s = s[:point] + "." + s[point:]
	}
	if strings.Contains(s, ".") {
		s = strings.TrimRight(strings.TrimRight(s, "0"), ".")
	}
	s = strings.TrimLeft(s, "0")
	if s == "" {
		return "0", nil
	}
	if s[0] == '.' {
		s = "0" + s
	}
	if negative {
		s = "-" + s
	}
	return s, nil
}
