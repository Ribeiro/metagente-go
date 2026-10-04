// Package value defines the values an agent works with. There are no user
// declared types: a value is nothing, yes or no, a number, a text, a list or
// a record.
package value

import (
	"math"
	"sort"
	"strconv"
	"strings"
)

// Kind tells what a Value holds.
type Kind int

const (
	KindNothing Kind = iota
	KindBool
	KindNumber
	KindText
	KindList
	KindRecord
)

// Value is one value. Only the field that matches Kind is meaningful. The
// zero Value is `nothing`.
type Value struct {
	Kind   Kind
	Bool   bool
	Number float64
	Text   string
	List   []Value
	Record map[string]Value
}

// Nothing is the value `nothing`.
var Nothing = Value{}

// Bool makes a yes/no value.
func Bool(b bool) Value { return Value{Kind: KindBool, Bool: b} }

// Number makes a number.
func Number(n float64) Value { return Value{Kind: KindNumber, Number: n} }

// Text makes a text.
func Text(s string) Value { return Value{Kind: KindText, Text: s} }

// List makes a list.
func List(items []Value) Value { return Value{Kind: KindList, List: items} }

// Record makes a record, like the result of a tool.
func Record(fields map[string]Value) Value { return Value{Kind: KindRecord, Record: fields} }

// Display says how the value reads inside a sentence.
func (v Value) Display() string {
	switch v.Kind {
	case KindBool:
		if v.Bool {
			return "yes"
		}
		return "no"
	case KindNumber:
		return formatNumber(v.Number)
	case KindText:
		return v.Text
	case KindList:
		parts := make([]string, len(v.List))
		for i, item := range v.List {
			parts[i] = item.Display()
		}
		return "[" + strings.Join(parts, ", ") + "]"
	case KindRecord:
		keys := sortedKeys(v.Record)
		parts := make([]string, len(keys))
		for i, key := range keys {
			parts[i] = key + ": " + v.Record[key].Display()
		}
		return "{" + strings.Join(parts, ", ") + "}"
	default:
		return "nothing"
	}
}

func formatNumber(n float64) string {
	if n == math.Trunc(n) && math.Abs(n) < 1e15 {
		return strconv.FormatInt(int64(n), 10)
	}
	return strconv.FormatFloat(n, 'f', -1, 64)
}

func sortedKeys(m map[string]Value) []string {
	keys := make([]string, 0, len(m))
	for key := range m {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

// Truthy says how the value counts as a condition: nothing, no, 0, an empty
// text, an empty list and an empty record are false.
func (v Value) Truthy() bool {
	switch v.Kind {
	case KindBool:
		return v.Bool
	case KindNumber:
		return v.Number != 0
	case KindText:
		return v.Text != ""
	case KindList:
		return len(v.List) > 0
	case KindRecord:
		return len(v.Record) > 0
	default:
		return false
	}
}

// AsNumber reads the value as a number. A text that looks like a number
// counts, so "5" from the command line compares with 5.
func (v Value) AsNumber() (float64, bool) {
	switch v.Kind {
	case KindNumber:
		return v.Number, true
	case KindText:
		n, err := strconv.ParseFloat(strings.TrimSpace(v.Text), 64)
		return n, err == nil
	default:
		return 0, false
	}
}

// Field returns a field of a record.
func (v Value) Field(name string) (Value, bool) {
	if v.Kind != KindRecord {
		return Nothing, false
	}
	field, ok := v.Record[name]
	return field, ok
}

// FieldNames lists the fields of a record in alphabetical order.
func (v Value) FieldNames() []string {
	if v.Kind != KindRecord {
		return nil
	}
	return sortedKeys(v.Record)
}

// Describe says what kind of value this is, for error messages.
func (v Value) Describe() string {
	switch v.Kind {
	case KindBool:
		return "a yes/no value"
	case KindNumber:
		return "a number"
	case KindText:
		return "text"
	case KindList:
		return "a list"
	case KindRecord:
		return "a record"
	default:
		return "nothing"
	}
}

// Equal compares two values deeply. A number and a text are different
// values here; the coercion of "5" to 5 belongs to the `is` comparison.
func Equal(a, b Value) bool {
	if a.Kind != b.Kind {
		return false
	}
	switch a.Kind {
	case KindBool:
		return a.Bool == b.Bool
	case KindNumber:
		return a.Number == b.Number
	case KindText:
		return a.Text == b.Text
	case KindList:
		return equalLists(a.List, b.List)
	case KindRecord:
		return equalRecords(a.Record, b.Record)
	}
	return true
}

func equalLists(a, b []Value) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if !Equal(a[i], b[i]) {
			return false
		}
	}
	return true
}

func equalRecords(a, b map[string]Value) bool {
	if len(a) != len(b) {
		return false
	}
	for key, av := range a {
		bv, ok := b[key]
		if !ok || !Equal(av, bv) {
			return false
		}
	}
	return true
}

// ToJSON turns the value into something encoding/json can write.
func (v Value) ToJSON() any {
	switch v.Kind {
	case KindBool:
		return v.Bool
	case KindNumber:
		if math.IsNaN(v.Number) || math.IsInf(v.Number, 0) {
			return nil
		}
		if v.Number == math.Trunc(v.Number) && math.Abs(v.Number) < 9e15 {
			return int64(v.Number)
		}
		return v.Number
	case KindText:
		return v.Text
	case KindList:
		items := make([]any, len(v.List))
		for i, item := range v.List {
			items[i] = item.ToJSON()
		}
		return items
	case KindRecord:
		fields := make(map[string]any, len(v.Record))
		for key, field := range v.Record {
			fields[key] = field.ToJSON()
		}
		return fields
	default:
		return nil
	}
}

// FromJSON turns what encoding/json decoded into an `any` into a Value.
func FromJSON(j any) Value {
	switch x := j.(type) {
	case nil:
		return Nothing
	case bool:
		return Bool(x)
	case float64:
		return Number(x)
	case string:
		return Text(x)
	case []any:
		items := make([]Value, len(x))
		for i, item := range x {
			items[i] = FromJSON(item)
		}
		return List(items)
	case map[string]any:
		fields := make(map[string]Value, len(x))
		for key, field := range x {
			fields[key] = FromJSON(field)
		}
		return Record(fields)
	default:
		return Nothing
	}
}
