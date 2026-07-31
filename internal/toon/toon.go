// Package toon encodes Go values as TOON (Token-Oriented Object Notation): a
// compact, LLM-friendly serialization that drops the repeated keys, quotes, and
// braces that make JSON token-expensive for the array-heavy responses MCP tools
// return.
//
// Key ideas:
//   - objects use YAML-style "key: value" with 2-space indentation;
//   - arrays of uniform scalar objects become a header + CSV-style rows, so each
//     field name is written once instead of per element;
//   - arrays of scalars are written inline as "key[N]: a,b,c".
//
// Encoding goes through the JSON token stream (preserving struct field order and
// integer formatting), so any json-marshalable value can be encoded and the
// output is deterministic.
package toon

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
)

// node is an order-preserving view of a JSON value.
type node struct {
	kind  kind
	str   string      // scalar string
	num   json.Number // scalar number
	b     bool        // scalar bool
	pairs []pair      // object
	items []*node     // array
}

type pair struct {
	key string
	val *node
}

type kind int

const (
	kNull kind = iota
	kString
	kNumber
	kBool
	kObject
	kArray
)

// Marshal encodes v as TOON. v must be json-marshalable.
func Marshal(v any) (string, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return "", err
	}
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.UseNumber()
	n, err := parse(dec)
	if err != nil {
		return "", err
	}
	var sb strings.Builder
	encodeRoot(&sb, n)
	return strings.TrimRight(sb.String(), "\n"), nil
}

func parse(dec *json.Decoder) (*node, error) {
	tok, err := dec.Token()
	if err != nil {
		return nil, err
	}
	return parseValue(dec, tok)
}

func parseValue(dec *json.Decoder, tok json.Token) (*node, error) {
	switch t := tok.(type) {
	case json.Delim:
		switch t {
		case '{':
			return parseObject(dec)
		case '[':
			return parseArray(dec)
		}
		return nil, fmt.Errorf("unexpected delim %v", t)
	case string:
		return &node{kind: kString, str: t}, nil
	case json.Number:
		return &node{kind: kNumber, num: t}, nil
	case bool:
		return &node{kind: kBool, b: t}, nil
	case nil:
		return &node{kind: kNull}, nil
	}
	return nil, fmt.Errorf("unexpected token %T", tok)
}

func parseObject(dec *json.Decoder) (*node, error) {
	n := &node{kind: kObject}
	for dec.More() {
		keyTok, err := dec.Token()
		if err != nil {
			return nil, err
		}
		key, ok := keyTok.(string)
		if !ok {
			return nil, fmt.Errorf("object key not a string")
		}
		valTok, err := dec.Token()
		if err != nil {
			return nil, err
		}
		val, err := parseValue(dec, valTok)
		if err != nil {
			return nil, err
		}
		n.pairs = append(n.pairs, pair{key: key, val: val})
	}
	if _, err := dec.Token(); err != nil { // consume '}'
		return nil, err
	}
	return n, nil
}

func parseArray(dec *json.Decoder) (*node, error) {
	n := &node{kind: kArray}
	for dec.More() {
		tok, err := dec.Token()
		if err != nil {
			return nil, err
		}
		item, err := parseValue(dec, tok)
		if err != nil {
			return nil, err
		}
		n.items = append(n.items, item)
	}
	if _, err := dec.Token(); err != nil { // consume ']'
		return nil, err
	}
	return n, nil
}

func encodeRoot(sb *strings.Builder, n *node) {
	switch n.kind {
	case kObject:
		encodeObjectBody(sb, n, 0)
	case kArray:
		encodeArray(sb, "data", n, 0)
	default:
		sb.WriteString(scalar(n))
		sb.WriteByte('\n')
	}
}

func indent(sb *strings.Builder, depth int) {
	for i := 0; i < depth; i++ {
		sb.WriteString("  ")
	}
}

func encodeObjectBody(sb *strings.Builder, n *node, depth int) {
	for _, p := range n.pairs {
		encodePair(sb, p.key, p.val, depth)
	}
}

func encodePair(sb *strings.Builder, key string, val *node, depth int) {
	switch val.kind {
	case kObject:
		if len(val.pairs) == 0 {
			indent(sb, depth)
			sb.WriteString(encodeKey(key))
			sb.WriteString(": {}\n")
			return
		}
		indent(sb, depth)
		sb.WriteString(encodeKey(key))
		sb.WriteString(":\n")
		encodeObjectBody(sb, val, depth+1)
	case kArray:
		encodeArray(sb, key, val, depth)
	default:
		indent(sb, depth)
		sb.WriteString(encodeKey(key))
		sb.WriteString(": ")
		sb.WriteString(scalar(val))
		sb.WriteByte('\n')
	}
}

func encodeArray(sb *strings.Builder, key string, n *node, depth int) {
	count := len(n.items)
	if count == 0 {
		indent(sb, depth)
		sb.WriteString(encodeKey(key))
		sb.WriteString("[0]:\n")
		return
	}
	// Tabular form: every item is an object with identical key order and all
	// scalar values. This is where TOON saves the most tokens.
	if fields, ok := tabularFields(n); ok {
		indent(sb, depth)
		sb.WriteString(encodeKey(key))
		sb.WriteString("[")
		sb.WriteString(strconv.Itoa(count))
		sb.WriteString("]{")
		sb.WriteString(strings.Join(mapEncodeKeys(fields), ","))
		sb.WriteString("}:\n")
		for _, item := range n.items {
			indent(sb, depth+1)
			cells := make([]string, len(fields))
			byKey := map[string]*node{}
			for _, p := range item.pairs {
				byKey[p.key] = p.val
			}
			for i, f := range fields {
				cells[i] = scalar(byKey[f])
			}
			sb.WriteString(strings.Join(cells, ","))
			sb.WriteByte('\n')
		}
		return
	}
	// Inline scalar list: key[N]: a,b,c
	if allScalar(n) {
		indent(sb, depth)
		sb.WriteString(encodeKey(key))
		sb.WriteString("[")
		sb.WriteString(strconv.Itoa(count))
		sb.WriteString("]: ")
		cells := make([]string, count)
		for i, item := range n.items {
			cells[i] = scalar(item)
		}
		sb.WriteString(strings.Join(cells, ","))
		sb.WriteByte('\n')
		return
	}
	// Mixed / nested fallback: list with "- " markers.
	indent(sb, depth)
	sb.WriteString(encodeKey(key))
	sb.WriteString("[")
	sb.WriteString(strconv.Itoa(count))
	sb.WriteString("]:\n")
	for _, item := range n.items {
		indent(sb, depth+1)
		sb.WriteString("- ")
		switch item.kind {
		case kObject:
			if len(item.pairs) == 0 {
				sb.WriteString("{}\n")
				continue
			}
			// First pair on the dash line, rest indented under it.
			sb.WriteString(encodeKey(item.pairs[0].key))
			sb.WriteString(": ")
			writeInlineOrBlock(sb, item.pairs[0].val, depth+2)
			for _, p := range item.pairs[1:] {
				encodePair(sb, p.key, p.val, depth+2)
			}
		case kArray:
			sb.WriteByte('\n')
			encodeArray(sb, "_", item, depth+2)
		default:
			sb.WriteString(scalar(item))
			sb.WriteByte('\n')
		}
	}
}

func writeInlineOrBlock(sb *strings.Builder, val *node, depth int) {
	switch val.kind {
	case kObject, kArray:
		sb.WriteByte('\n')
		if val.kind == kObject {
			encodeObjectBody(sb, val, depth)
		} else {
			encodeArray(sb, "_", val, depth)
		}
	default:
		sb.WriteString(scalar(val))
		sb.WriteByte('\n')
	}
}

// tabularFields returns the shared field order if n is a non-empty array of
// objects that all share the same keys with scalar values.
func tabularFields(n *node) ([]string, bool) {
	if len(n.items) == 0 {
		return nil, false
	}
	var fields []string
	first := true
	for _, item := range n.items {
		if item.kind != kObject || len(item.pairs) == 0 {
			return nil, false
		}
		keys := make([]string, 0, len(item.pairs))
		for _, p := range item.pairs {
			if p.val.kind == kObject || p.val.kind == kArray {
				return nil, false // non-scalar cell
			}
			keys = append(keys, p.key)
		}
		if first {
			fields = keys
			first = false
			continue
		}
		if !sameOrder(fields, keys) {
			return nil, false
		}
	}
	return fields, true
}

func sameOrder(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func allScalar(n *node) bool {
	for _, item := range n.items {
		if item.kind == kObject || item.kind == kArray {
			return false
		}
	}
	return true
}

func mapEncodeKeys(keys []string) []string {
	out := make([]string, len(keys))
	for i, k := range keys {
		out[i] = encodeKey(k)
	}
	return out
}

func scalar(n *node) string {
	if n == nil {
		return ""
	}
	switch n.kind {
	case kNull:
		return "null"
	case kBool:
		if n.b {
			return "true"
		}
		return "false"
	case kNumber:
		return n.num.String()
	case kString:
		return encodeString(n.str)
	}
	return ""
}

// encodeKey quotes a key only when it contains separators that would break parsing.
func encodeKey(k string) string {
	if k == "" || strings.ContainsAny(k, ":,{}[]\"\n ") {
		return strconv.Quote(k)
	}
	return k
}

// encodeString emits a bare string when unambiguous, else a JSON-quoted string.
func encodeString(s string) string {
	if needsQuote(s) {
		return strconv.Quote(s)
	}
	return s
}

func needsQuote(s string) bool {
	if s == "" {
		return true
	}
	if s != strings.TrimSpace(s) {
		return true
	}
	if strings.ContainsAny(s, ",:{}[]\"\n\t") {
		return true
	}
	switch s {
	case "true", "false", "null":
		return true
	}
	// Quote if it would be mistaken for a number.
	if _, err := strconv.ParseFloat(s, 64); err == nil {
		return true
	}
	return false
}
