package tools

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"time"

	"github.com/Ribeiro/metagente-go/internal/config"
	"github.com/Ribeiro/metagente-go/internal/diag"
	"github.com/Ribeiro/metagente-go/internal/lang"
	"github.com/Ribeiro/metagente-go/internal/value"
)

// Codec is `tool codec`: the small pieces that a pipeline needs to move rows between two programs. It reads
// and writes JSON, makes records, packs and unpacks text with gzip, takes a SHA-256, and makes UUIDs. It
// touches nothing outside the values it is given, so it needs no approval. Every text it reads or writes
// is limited by max_data_bytes, and a compressed text is limited once unpacked, because a small one can
// hide a huge one.
type Codec struct {
	decl  *lang.ToolDecl
	limit int64
}

// NewCodec creates the tool.
func NewCodec(decl *lang.ToolDecl, limits config.Limits) *Codec {
	return &Codec{decl: decl, limit: limits.MaxDataBytes}
}

func (d *Codec) Name() string { return d.decl.Name }

func (d *Codec) Actions(context.Context) ([]lang.ActionInfo, error) {
	return lang.BuiltinActions(d.decl), nil
}

func (d *Codec) Call(_ context.Context, action string, args Args) (value.Value, error) {
	switch action {
	case "record":
		return value.Record(args), nil
	case "table":
		return d.table(args)
	case "records":
		return d.records(args)
	case "json":
		text, err := d.encode(action, args["value"])
		return value.Text(text), err
	case "count":
		list := args["value"]
		if list.Kind != value.KindList {
			return value.Nothing, diag.Newf("`%s.count` needs a list, and it is %s", d.decl.Name, list.Describe())
		}
		return value.Number(float64(len(list.List))), nil
	case "size":
		text, err := d.encode(action, args["value"])
		return value.Number(float64(len(text))), err
	case "parse":
		return d.parse(args)
	case "pick":
		return d.pick(args)
	case "try_parse":
		got, err := d.parse(args)
		if err != nil {
			if _, isText := args["text"]; isText && args["text"].Kind == value.KindText && int64(len(args["text"].Text)) <= d.limit {
				return value.Nothing, nil // not JSON: nothing, and the author decides
			}
			return value.Nothing, err
		}
		return got, nil
	case "gzip":
		return d.gzip(args)
	case "gunzip":
		return d.gunzip(args)
	case "sha256":
		text, err := d.text(action, args)
		sum := sha256.Sum256([]byte(text))
		return value.Text(hex.EncodeToString(sum[:])), err
	case "uuid":
		return newUUIDv7()
	}
	return value.Nothing, UnknownAction(d.decl.Name, action, []string{"record", "table", "records", "json", "parse", "try_parse", "pick", "count", "size", "gzip", "gunzip", "sha256", "uuid"})
}

// text reads the text value of an action, within the limit.
func (d *Codec) text(action string, args Args) (string, error) {
	v, ok := args["text"]
	if !ok || v.Kind != value.KindText {
		return "", diag.Newf("`%s.%s` needs a text for `text`", d.decl.Name, action).
			Fixf("write it like: %s.%s text: \"...\"", d.decl.Name, action)
	}
	if int64(len(v.Text)) > d.limit {
		return "", d.tooLarge(action)
	}
	return v.Text, nil
}

func (d *Codec) tooLarge(action string) error {
	return diag.Newf("`%s.%s` passes the %d bytes that `tool codec` takes", d.decl.Name, action, d.limit).
		Fix("work with smaller parts, or raise max_data_bytes in the [limits] section of metagente.toml")
}

// encode writes a value as JSON, within the limit.
func (d *Codec) encode(action string, v value.Value) (string, error) {
	var out limitedBuffer
	out.limit = d.limit
	enc := json.NewEncoder(&out)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v.ToJSON()); err != nil {
		if out.exceeded {
			return "", d.tooLarge(action)
		}
		return "", diag.Newf("`%s.%s` could not write the value as JSON: %s", d.decl.Name, action, err.Error())
	}
	return string(bytes.TrimRight(out.Bytes(), "\n")), nil
}

// limitedBuffer refuses to hold more than limit bytes.
type limitedBuffer struct {
	bytes.Buffer
	limit    int64
	exceeded bool
}

func (b *limitedBuffer) Write(p []byte) (int, error) {
	if int64(b.Len()+len(p)) > b.limit {
		b.exceeded = true
		return 0, fmt.Errorf("it passes %d bytes", b.limit)
	}
	return b.Buffer.Write(p)
}

func (d *Codec) parse(args Args) (value.Value, error) {
	text, err := d.text("parse", args)
	if err != nil {
		return value.Nothing, err
	}
	dec := json.NewDecoder(bytes.NewReader([]byte(text)))
	var decoded any
	if err := dec.Decode(&decoded); err != nil {
		return value.Nothing, diag.Newf("`%s.parse` could not read the text as JSON: %s", d.decl.Name, jsonProblem(err)).
			Fix("check that the text is JSON")
	}
	if _, err := dec.Token(); err != io.EOF {
		return value.Nothing, diag.Newf("`%s.parse` found more after the first JSON value", d.decl.Name).
			Fix("give one JSON value")
	}
	return value.FromJSON(decoded), nil
}

// jsonProblem says what is wrong with a JSON text, without repeating the text: it may hold personal data.
func jsonProblem(err error) string {
	switch e := err.(type) {
	case *json.SyntaxError:
		return fmt.Sprintf("it is not valid near byte %d", e.Offset)
	case *json.UnmarshalTypeError:
		return "a value has the wrong kind"
	}
	return "the text ends too soon"
}

func (d *Codec) gzip(args Args) (value.Value, error) {
	text, err := d.text("gzip", args)
	if err != nil {
		return value.Nothing, err
	}
	var packed bytes.Buffer
	zw := gzip.NewWriter(&packed)
	if _, err := zw.Write([]byte(text)); err != nil {
		return value.Nothing, diag.Newf("`%s.gzip` could not compress: %s", d.decl.Name, err.Error())
	}
	if err := zw.Close(); err != nil {
		return value.Nothing, diag.Newf("`%s.gzip` could not compress: %s", d.decl.Name, err.Error())
	}
	return value.Text(base64.StdEncoding.EncodeToString(packed.Bytes())), nil
}

func (d *Codec) gunzip(args Args) (value.Value, error) {
	text, err := d.text("gunzip", args)
	if err != nil {
		return value.Nothing, err
	}
	raw, err := base64.StdEncoding.DecodeString(text)
	if err != nil {
		return value.Nothing, diag.Newf("`%s.gunzip` could not read the text as base64", d.decl.Name).
			Fix("give the text that `gzip` made")
	}
	zr, err := gzip.NewReader(bytes.NewReader(raw))
	if err != nil {
		return value.Nothing, diag.Newf("`%s.gunzip` could not read the text as gzip", d.decl.Name).
			Fix("give the text that `gzip` made")
	}
	defer zr.Close()
	unpacked, err := io.ReadAll(io.LimitReader(zr, d.limit+1))
	if err != nil {
		return value.Nothing, diag.Newf("`%s.gunzip` could not unpack the text: it is damaged", d.decl.Name)
	}
	if int64(len(unpacked)) > d.limit {
		return value.Nothing, d.tooLarge("gunzip")
	}
	return value.Text(string(unpacked)), nil
}

// names reads the list of columns of an action.
func (d *Codec) names(action string, v value.Value) ([]string, error) {
	if v.Kind != value.KindList || len(v.List) == 0 {
		return nil, diag.Newf("`%s.%s` needs `columns` to be a list of names", d.decl.Name, action).
			Fixf("write it like: %s.%s rows: rows columns: [\"id\", \"name\"]", d.decl.Name, action)
	}
	names := make([]string, len(v.List))
	seen := map[string]bool{}
	for i, item := range v.List {
		if item.Kind != value.KindText || item.Text == "" || seen[item.Text] {
			return nil, diag.Newf("`%s.%s` needs `columns` to be different names in quotes", d.decl.Name, action)
		}
		names[i], seen[item.Text] = item.Text, true
	}
	return names, nil
}

func (d *Codec) rowsOf(action string, args Args) ([]value.Value, error) {
	rows := args["rows"]
	if rows.Kind != value.KindList {
		return nil, diag.Newf("`%s.%s` needs `rows` to be a list, and it is %s", d.decl.Name, action, rows.Describe()).
			Fix("give the list that a statement of `tool sql` answered")
	}
	return rows.List, nil
}

// table turns records into lists of values in the order of the columns: the names are said once, not in
// every row.
func (d *Codec) table(args Args) (value.Value, error) {
	columns, err := d.names("table", args["columns"])
	if err != nil {
		return value.Nothing, err
	}
	rows, err := d.rowsOf("table", args)
	if err != nil {
		return value.Nothing, err
	}
	out := make([]value.Value, len(rows))
	for i, row := range rows {
		if row.Kind != value.KindRecord {
			return value.Nothing, diag.Newf("`%s.table`: row %d is %s, and a record is needed", d.decl.Name, i+1, row.Describe())
		}
		cells := make([]value.Value, len(columns))
		for j, column := range columns {
			cell, ok := row.Record[column]
			if !ok {
				return value.Nothing, diag.Newf("`%s.table`: row %d has no field called `%s`", d.decl.Name, i+1, column).
					Fix("name the columns that the rows have")
			}
			cells[j] = cell
		}
		out[i] = value.List(cells)
	}
	return value.List(out), nil
}

// records is the opposite of table.
func (d *Codec) records(args Args) (value.Value, error) {
	columns, err := d.names("records", args["columns"])
	if err != nil {
		return value.Nothing, err
	}
	rows, err := d.rowsOf("records", args)
	if err != nil {
		return value.Nothing, err
	}
	out := make([]value.Value, len(rows))
	for i, row := range rows {
		if row.Kind != value.KindList || len(row.List) != len(columns) {
			return value.Nothing, diag.Newf("`%s.records`: row %d has to be a list of %d values", d.decl.Name, i+1, len(columns))
		}
		fields := make(map[string]value.Value, len(columns))
		for j, column := range columns {
			fields[column] = row.List[j]
		}
		out[i] = value.Record(fields)
	}
	return value.List(out), nil
}

// newUUIDv7 makes a UUID of version 7: 48 bits of the time in milliseconds, then random bits.
func newUUIDv7() (value.Value, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return value.Nothing, diag.Newf("I could not get random numbers to make a UUID: %s", err.Error())
	}
	var ms [8]byte
	binary.BigEndian.PutUint64(ms[:], uint64(time.Now().UnixMilli()))
	copy(b[:6], ms[2:])
	b[6] = 0x70 | b[6]&0x0f
	b[8] = 0x80 | b[8]&0x3f
	h := hex.EncodeToString(b[:])
	return value.Text(h[0:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:]), nil
}

// maxPicked is the most records that pick gives, so that an answer of a model cannot be a list without an end.
const maxPicked = 10000

// pick keeps from a list the records that have every field named, with only those fields and only when their
// values are texts, numbers or yes and no. It is how an agent reads what a language model wrote: the answer is
// a text that nobody checked, and what is not in the shape wanted is left out, with no problem.
func (d *Codec) pick(args Args) (value.Value, error) {
	fields, err := d.names("pick", args["fields"])
	if err != nil {
		return value.Nothing, err
	}
	kept := []value.Value{}
	rows := args["rows"]
	if rows.Kind != value.KindList {
		return value.List(kept), nil
	}
	for _, row := range rows.List {
		if len(kept) >= maxPicked {
			break
		}
		if record, ok := pickRecord(row, fields); ok {
			kept = append(kept, record)
		}
	}
	return value.List(kept), nil
}

func pickRecord(row value.Value, fields []string) (value.Value, bool) {
	if row.Kind != value.KindRecord {
		return value.Nothing, false
	}
	picked := make(map[string]value.Value, len(fields))
	for _, field := range fields {
		v, ok := row.Record[field]
		if !ok || (v.Kind != value.KindText && v.Kind != value.KindNumber && v.Kind != value.KindBool) {
			return value.Nothing, false
		}
		picked[field] = v
	}
	return value.Record(picked), true
}
