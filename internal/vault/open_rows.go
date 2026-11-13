// SPDX-License-Identifier: AGPL-3.0-only

package vault

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"
)

// RowIdentity is the identity of a row of t, the tail of its AAD: the values
// of t.Identity in order (get returns the value of a column), a string or
// byte column as its bytes, an instant or a date as its UnixMicro (uint64 big
// endian, the grain Postgres keeps), an integer as 8 bytes big endian, a
// boolean as one byte and a UUID as its 16 bytes. It is the encoding the ETL
// writer seals with; for the samples it is content_hash || start_date.
func RowIdentity(t Table, get func(column string) any) ([]byte, error) {
	var out []byte
	for _, c := range t.Identity {
		switch v := get(c).(type) {
		case string:
			out = append(out, v...)
		case []byte:
			out = append(out, v...)
		case time.Time:
			out = binary.BigEndian.AppendUint64(out, uint64(v.UnixMicro()))
		case int64:
			out = binary.BigEndian.AppendUint64(out, uint64(v))
		case int32:
			out = binary.BigEndian.AppendUint64(out, uint64(int64(v)))
		case int16:
			out = binary.BigEndian.AppendUint64(out, uint64(int64(v)))
		case int:
			out = binary.BigEndian.AppendUint64(out, uint64(int64(v)))
		case bool:
			if v {
				out = append(out, 1)
			} else {
				out = append(out, 0)
			}
		case uuid.UUID:
			out = append(out, v[:]...)
		case [16]byte:
			out = append(out, v[:]...)
		default:
			return nil, fmt.Errorf("vault: %s identity column %s is %T", t.Name, c, v)
		}
	}
	return out, nil
}

// Scanner is one row of a result: pgx.Row, pgx.Rows and pgx.CollectableRow
// all fit.
type Scanner interface {
	Scan(dest ...any) error
}

// Columns is a select list over one sealed table that reads a row whether
// it is sealed or still clear: the caller's columns, then the identity
// columns of the table and its sealed column. Scan fills the caller's
// destinations from the clear columns of a clear row and from the opened
// value of a sealed one, so the code above it keeps its structs.
type Columns struct {
	t     Table
	items []columnItem
	// sealedOnly is a table born sealed: its sealed columns exist only
	// inside the sealed value, and are selected as NULL.
	sealedOnly bool
}

type columnItem struct {
	expr   string // as the caller wrote it
	plain  bool   // a bare column name, which takes the alias
	sealed int    // position in t.Sealed, -1 for a clear column
}

var bareColumn = regexp.MustCompile(`^"?[a-z_][a-z0-9_]*"?$`)

// Select describes the read of cols, a comma separated list of columns of
// table (a name of Schema), in the order of the destinations Scan takes. An
// item that is not a bare column name (NULL::text) is selected as written
// and is never a sealed column. It panics on a table the vault does not
// describe: a programming error the first test finds.
func Select(table, cols string) *Columns {
	t, ok := Schema[table]
	if !ok {
		panic("vault: no schema for " + table)
	}
	c := &Columns{t: t}
	for _, raw := range strings.Split(cols, ",") {
		expr := strings.TrimSpace(raw)
		if expr == "" {
			continue
		}
		it := columnItem{expr: expr, plain: bareColumn.MatchString(expr), sealed: -1}
		if it.plain {
			name := strings.Trim(expr, `"`)
			it.sealed = slices.IndexFunc(t.Sealed, func(s Column) bool { return s.Name == name })
		}
		c.items = append(c.items, it)
	}
	return c
}

// SelectSealedOnly is Select for a table that never had clear value
// columns (quantity_hourly_sealed): its sealed columns live only inside the
// sealed value, so every row opens.
func SelectSealedOnly(table, cols string) *Columns {
	c := Select(table, cols)
	c.sealedOnly = true
	return c
}

// nullOf is the typed NULL selected in place of a sealed column of kind k
// that the table does not have.
func nullOf(k Kind) string {
	switch k {
	case KindFloat64:
		return "NULL::float8"
	case KindInt64:
		return "NULL::bigint"
	case KindBool:
		return "NULL::boolean"
	case KindText:
		return "NULL::text"
	case KindTime:
		return "NULL::timestamptz"
	case KindJSON:
		return "NULL::jsonb"
	}
	return "NULL::bytea"
}

// Table is the table the columns read.
func (c *Columns) Table() Table { return c.t }

// List is the SQL select list: the caller's columns, the identity columns,
// then sealed. alias, when not empty, qualifies every bare column.
func (c *Columns) List(alias string) string {
	prefix := ""
	if alias != "" {
		prefix = alias + "."
	}
	parts := make([]string, 0, len(c.items)+len(c.t.Identity)+1)
	for _, it := range c.items {
		switch {
		case c.sealedOnly && it.sealed >= 0:
			parts = append(parts, nullOf(c.t.Sealed[it.sealed].Kind))
		case it.plain:
			parts = append(parts, prefix+it.expr)
		default:
			parts = append(parts, it.expr)
		}
	}
	for _, id := range c.t.Identity {
		parts = append(parts, prefix+`"`+id+`"`)
	}
	return strings.Join(append(parts, prefix+"sealed"), ", ")
}

// Scan reads one row selected with List (and, after it, the caller's own
// extra expressions, scanned into extra): dest takes the caller's columns
// in the order of Select. A sealed row is opened through rd; its failure to
// open is an *OpenError, never a row with empty values. A destination that
// cannot be NULL (a float64, an int, a string) refuses a NULL value, as a
// scan of the clear column does.
func (c *Columns) Scan(ctx context.Context, rd *Reader, row Scanner, dest []any, extra ...any) error {
	if len(dest) != len(c.items) {
		return fmt.Errorf("vault: %s: %d destinations for %d columns", c.t.Name, len(dest), len(c.items))
	}
	targets := make([]any, 0, len(dest)+len(c.t.Identity)+1+len(extra))
	holders := make([]any, len(c.items))
	for i, it := range c.items {
		if it.sealed < 0 {
			targets = append(targets, dest[i])
			continue
		}
		holders[i] = holderOf(c.t.Sealed[it.sealed].Kind)
		targets = append(targets, holders[i])
	}
	ids := make([]any, len(c.t.Identity))
	for i := range ids {
		targets = append(targets, &ids[i])
	}
	var sealed []byte
	targets = append(append(targets, &sealed), extra...)
	if err := row.Scan(targets...); err != nil {
		return err
	}
	var opened []any
	if sealed != nil {
		id, err := RowIdentity(c.t, func(col string) any {
			return ids[slices.Index(c.t.Identity, col)]
		})
		if err != nil {
			return err
		}
		if opened, err = rd.Open(ctx, c.t, id, sealed); err != nil {
			return err
		}
	}
	for i, it := range c.items {
		if it.sealed < 0 {
			continue
		}
		var v any
		if opened != nil {
			v = opened[it.sealed]
			if raw, ok := v.(json.RawMessage); ok && c.t.Sealed[it.sealed].Kind == KindJSON {
				v = jsonbValue(raw)
			}
		} else {
			v = heldValue(holders[i])
		}
		if err := assign(dest[i], v); err != nil {
			return fmt.Errorf("vault: %s.%s: %w", c.t.Name, c.t.Sealed[it.sealed].Name, err)
		}
	}
	return nil
}

// holderOf is where a clear sealed column of kind k is scanned: a NULL (the
// column of a sealed row) leaves it nil.
func holderOf(k Kind) any {
	switch k {
	case KindFloat64:
		return new(*float64)
	case KindInt64:
		return new(*int64)
	case KindBool:
		return new(*bool)
	case KindText:
		return new(*string)
	case KindTime:
		return new(*time.Time)
	default: // KindJSON, KindBytes
		return new([]byte)
	}
}

// heldValue is the value a holder took, as Table.Decode would return it.
func heldValue(h any) any {
	switch x := h.(type) {
	case **float64:
		if *x != nil {
			return **x
		}
	case **int64:
		if *x != nil {
			return **x
		}
	case **bool:
		if *x != nil {
			return **x
		}
	case **string:
		if *x != nil {
			return **x
		}
	case **time.Time:
		if *x != nil {
			return (**x).UTC()
		}
	case *[]byte:
		if *x != nil {
			return json.RawMessage(*x)
		}
	}
	return nil
}

// jsonbValue is a sealed JSON document as its clear jsonb column would read:
// the text Postgres prints for it, and NULL for the JSON null a nil document
// was sealed as (a driver writes a nil map as SQL NULL).
func jsonbValue(raw json.RawMessage) any {
	out := JSONBText(raw)
	if bytes.Equal(out, []byte("null")) {
		return nil
	}
	return json.RawMessage(out)
}

// assign stores v, a value of Table.Decode or nil, into dest.
func assign(dest, v any) error {
	switch d := dest.(type) {
	case *any:
		*d = v
		return nil
	case **float64:
		return setPtr(d, v)
	case **int64:
		return setPtr(d, v)
	case **bool:
		return setPtr(d, v)
	case **string:
		return setPtr(d, v)
	case **int:
		if v == nil {
			*d = nil
			return nil
		}
		n, ok := v.(int64)
		if !ok {
			return fmt.Errorf("cannot store %T in an int", v)
		}
		i := int(n)
		*d = &i
		return nil
	case *float64:
		return setVal(d, v)
	case *int64:
		return setVal(d, v)
	case *bool:
		return setVal(d, v)
	case *string:
		return setVal(d, v)
	case *int:
		n, ok := v.(int64)
		if !ok {
			return notStored(v, "an int")
		}
		*d = int(n)
		return nil
	case *json.RawMessage:
		switch x := v.(type) {
		case nil:
			*d = nil
		case json.RawMessage:
			*d = x
		default:
			return notStored(v, "a JSON document")
		}
		return nil
	case *[]byte:
		switch x := v.(type) {
		case nil:
			*d = nil
		case json.RawMessage:
			*d = x
		case []byte:
			*d = x
		default:
			return notStored(v, "bytes")
		}
		return nil
	}
	return fmt.Errorf("unsupported destination %T", dest)
}

func setPtr[T any](d **T, v any) error {
	if v == nil {
		*d = nil
		return nil
	}
	x, ok := v.(T)
	if !ok {
		return fmt.Errorf("cannot store %T in a %T", v, *new(T))
	}
	*d = &x
	return nil
}

func setVal[T any](d *T, v any) error {
	x, ok := v.(T)
	if !ok {
		return notStored(v, fmt.Sprintf("a %T", *new(T)))
	}
	*d = x
	return nil
}

func notStored(v any, what string) error {
	if v == nil {
		return fmt.Errorf("cannot store NULL in %s", what)
	}
	return fmt.Errorf("cannot store %T in %s", v, what)
}

// JSONBText returns the text Postgres prints for the jsonb value of a JSON
// document: object keys once each (the last wins), ordered by length then
// bytes, ": " and ", " as separators, numbers without an exponent. A sealed
// document holds the bytes the writer gave; a clear jsonb column holds the
// same document in this form, and a reader must not see the difference (a
// CSV export prints the text). A document that does not parse is returned
// as it is.
func JSONBText(raw []byte) []byte {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil || dec.More() {
		return raw
	}
	return appendJSONB(make([]byte, 0, len(raw)+8), v)
}

func appendJSONB(out []byte, v any) []byte {
	switch x := v.(type) {
	case nil:
		return append(out, "null"...)
	case bool:
		if x {
			return append(out, "true"...)
		}
		return append(out, "false"...)
	case json.Number:
		return append(out, jsonbNumber(string(x))...)
	case string:
		return appendJSONBString(out, x)
	case []any:
		out = append(out, '[')
		for i, e := range x {
			if i > 0 {
				out = append(out, ", "...)
			}
			out = appendJSONB(out, e)
		}
		return append(out, ']')
	case map[string]any:
		keys := make([]string, 0, len(x))
		for k := range x {
			keys = append(keys, k)
		}
		slices.SortFunc(keys, func(a, b string) int {
			if len(a) != len(b) {
				return len(a) - len(b)
			}
			return strings.Compare(a, b)
		})
		out = append(out, '{')
		for i, k := range keys {
			if i > 0 {
				out = append(out, ", "...)
			}
			out = appendJSONBString(out, k)
			out = append(out, ": "...)
			out = appendJSONB(out, x[k])
		}
		return append(out, '}')
	}
	return out
}

// appendJSONBString escapes s as Postgres does: the quote, the backslash and
// the control characters, nothing else.
func appendJSONBString(out []byte, s string) []byte {
	const hex = "0123456789abcdef"
	out = append(out, '"')
	for i := 0; i < len(s); i++ {
		switch b := s[i]; b {
		case '"':
			out = append(out, '\\', '"')
		case '\\':
			out = append(out, '\\', '\\')
		case '\b':
			out = append(out, '\\', 'b')
		case '\f':
			out = append(out, '\\', 'f')
		case '\n':
			out = append(out, '\\', 'n')
		case '\r':
			out = append(out, '\\', 'r')
		case '\t':
			out = append(out, '\\', 't')
		default:
			if b < 0x20 {
				out = append(out, '\\', 'u', '0', '0', hex[b>>4], hex[b&0xf])
			} else {
				out = append(out, b)
			}
		}
	}
	return append(out, '"')
}

// jsonbNumber is a JSON number as the numeric Postgres stores it prints: the
// digits as written, an exponent applied, no negative zero.
func jsonbNumber(s string) string {
	neg := strings.HasPrefix(s, "-")
	if neg {
		s = s[1:]
	}
	mant, exp := s, 0
	if i := strings.IndexAny(s, "eE"); i >= 0 {
		mant = s[:i]
		e := 0
		if _, err := fmt.Sscanf(s[i+1:], "%d", &e); err != nil || e > 100000 || e < -100000 {
			if neg {
				return "-" + s
			}
			return s
		}
		exp = e
	}
	whole, frac, _ := strings.Cut(mant, ".")
	switch {
	case exp > 0:
		if exp >= len(frac) {
			whole += frac + strings.Repeat("0", exp-len(frac))
			frac = ""
		} else {
			whole, frac = whole+frac[:exp], frac[exp:]
		}
	case exp < 0:
		if -exp >= len(whole) {
			frac = strings.Repeat("0", -exp-len(whole)) + whole + frac
			whole = "0"
		} else {
			cut := len(whole) + exp
			whole, frac = whole[:cut], whole[cut:]+frac
		}
	}
	whole = strings.TrimLeft(whole, "0")
	if whole == "" {
		whole = "0"
	}
	out := whole
	if frac != "" {
		out += "." + frac
	}
	if neg && strings.Trim(out, "0.") != "" {
		out = "-" + out
	}
	return out
}
