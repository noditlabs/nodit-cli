package output

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"
)

func TestFormatsPreserveNumbersAndStrings(t *testing.T) {
	v := map[string]any{"data": map[string]any{"integer": json.Number("9007199254740993123456789"), "numericString": "9007199254740993123456789", "fraction": json.Number("0.12345678901234567890123456789"), "falseString": "false", "empty": []any{}}}
	for _, format := range []string{"yaml", "json", "jsonl", "toon"} {
		t.Run(format, func(t *testing.T) {
			var b bytes.Buffer
			if err := Write(&b, format, v); err != nil {
				t.Fatal(err)
			}
			text := b.String()
			if !strings.Contains(text, "9007199254740993123456789") || !strings.Contains(text, "0.12345678901234567890123456789") || !strings.Contains(text, `"false"`) {
				t.Fatal(text)
			}
			if format == "jsonl" && strings.Count(text, "\n") != 1 {
				t.Fatal("JSONL must wrap the complete response in one line")
			}
			if format == "yaml" {
				var doc yaml.Node
				if err := yaml.Unmarshal(b.Bytes(), &doc); err != nil {
					t.Fatal(err)
				}
				fields := doc.Content[0].Content[1].Content
				for i := 0; i < len(fields); i += 2 {
					if fields[i].Value == "integer" && fields[i+1].Tag != "!!int" {
						t.Fatal("integer changed type")
					}
					if fields[i].Value == "numericString" && fields[i+1].Tag != "!!str" {
						t.Fatal("string changed type")
					}
				}
			}
		})
	}
}

func TestDecimalCanonicalization(t *testing.T) {
	for input, want := range map[string]string{"1e3": "1000", "1e-3": "0.001", "-0.00": "0", "123.4500": "123.45", "900719925474099312345e-2": "9007199254740993123.45", "-1.2e+3": "-1200"} {
		got, err := decimal(input)
		if err != nil || got != want {
			t.Fatalf("%s: %s %v", input, got, err)
		}
	}
}

func TestTOONNestedValues(t *testing.T) {
	var b bytes.Buffer
	value := map[string]any{"data": []any{map[string]any{"id": "solana-devnet", "products": []string{"node"}}, map[string]any{"id": "aptos-testnet", "products": []string{"node", "data"}}}}
	if err := Write(&b, "toon", value); err != nil {
		t.Fatal(err)
	}
	want := "\"data\"[2]:\n  - \"id\": \"solana-devnet\"\n    \"products\"[1]: \"node\"\n  - \"id\": \"aptos-testnet\"\n    \"products\"[2]: \"node\",\"data\"\n"
	if b.String() != want {
		t.Fatalf("got:\n%s\nwant:\n%s", b.String(), want)
	}
}

func TestTOONTabularValues(t *testing.T) {
	var b bytes.Buffer
	v := map[string]any{"data": []any{map[string]any{"id": 1, "name": "first"}, map[string]any{"id": 2, "name": "second"}}}
	if err := Write(&b, "toon", v); err != nil {
		t.Fatal(err)
	}
	want := "\"data\"[2]{\"id\",\"name\"}:\n  1,\"first\"\n  2,\"second\"\n"
	if b.String() != want {
		t.Fatalf("unexpected table: %s", b.String())
	}
}

// The default format is YAML, where sorting alphabetically would open on apiCode and bury message.
func TestErrorFieldsKeepTheDocumentedOrder(t *testing.T) {
	value := map[string]any{"error": map[string]any{
		"apiCode":    "NOT_FOUND",
		"code":       "API_ERROR",
		"httpStatus": 404,
		"message":    "not found",
		"details":    map[string]any{"b": 2, "a": 1},
	}}
	for _, tc := range []struct{ format, want string }{
		{"yaml", "error:\n  code: \"API_ERROR\"\n  message: \"not found\"\n  httpStatus: 404\n" +
			"  apiCode: \"NOT_FOUND\"\n  details:\n    a: 1\n    b: 2\n"},
		{"toon", "\"error\":\n  \"code\": \"API_ERROR\"\n  \"message\": \"not found\"\n  \"httpStatus\": 404\n" +
			"  \"apiCode\": \"NOT_FOUND\"\n  \"details\":\n    \"a\": 1\n    \"b\": 2\n"},
	} {
		var b bytes.Buffer
		if err := Write(&b, tc.format, value); err != nil {
			t.Fatal(err)
		}
		if b.String() != tc.want {
			t.Fatalf("%s got:\n%s\nwant:\n%s", tc.format, b.String(), tc.want)
		}
	}
}

// A server response has no contracted field order, so alphabetical keeps one command's output stable.
func TestDataKeysStayAlphabetical(t *testing.T) {
	var b bytes.Buffer
	value := map[string]any{"data": map[string]any{"message": "m", "code": "c", "apiCode": "a"}}
	if err := Write(&b, "yaml", value); err != nil {
		t.Fatal(err)
	}
	want := "data:\n  apiCode: \"a\"\n  code: \"c\"\n  message: \"m\"\n"
	if b.String() != want {
		t.Fatalf("got:\n%s\nwant:\n%s", b.String(), want)
	}
}
