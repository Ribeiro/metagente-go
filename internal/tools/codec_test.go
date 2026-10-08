package tools

import (
	"context"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/Ribeiro/metagente-go/internal/config"
	"github.com/Ribeiro/metagente-go/internal/lang"
	"github.com/Ribeiro/metagente-go/internal/value"
)

func codec(t *testing.T, limit int64) *Codec {
	t.Helper()
	limits := config.Default().Limits
	if limit > 0 {
		limits.MaxDataBytes = limit
	}
	return NewCodec(&lang.ToolDecl{Name: "codec", Kind: lang.ToolCodec}, limits)
}

func call(t *testing.T, c *Codec, action string, args Args) value.Value {
	t.Helper()
	got, err := c.Call(context.Background(), action, args)
	if err != nil {
		t.Fatalf("%s: %s", action, rendered(t, err))
	}
	return got
}

func fail(t *testing.T, c *Codec, action string, args Args, want string) {
	t.Helper()
	_, err := c.Call(context.Background(), action, args)
	if err == nil || !strings.Contains(rendered(t, err), want) {
		t.Errorf("%s: want a problem with %q, got %v", action, want, err)
	}
}

func text(s string) value.Value { return value.Text(s) }

func names(items ...string) value.Value {
	list := make([]value.Value, len(items))
	for i, item := range items {
		list[i] = text(item)
	}
	return value.List(list)
}

func TestARecordIsMadeFromTheValuesGiven(t *testing.T) {
	c := codec(t, 0)
	got := call(t, c, "record", Args{"v": value.Number(1), "job": text("j")})
	if v, _ := got.Field("v"); v.Number != 1 {
		t.Errorf("record = %s", got.Display())
	}
	if job, _ := got.Field("job"); job.Text != "j" {
		t.Errorf("record = %s", got.Display())
	}
	if empty := call(t, c, "record", Args{}); empty.Kind != value.KindRecord {
		t.Errorf("empty = %s", empty.Display())
	}
}

func TestRowsGoToListsAndBackWithTheNamesOnce(t *testing.T) {
	c := codec(t, 0)
	rows := value.List([]value.Value{
		value.Record(map[string]value.Value{"id": value.Number(1), "customer": text("Ana"), "extra": text("x")}),
		value.Record(map[string]value.Value{"id": value.Number(2), "customer": text("Bob"), "extra": text("y")}),
	})
	table := call(t, c, "table", Args{"rows": rows, "columns": names("id", "customer")})
	if len(table.List) != 2 || table.List[1].List[0].Number != 2 || table.List[1].List[1].Text != "Bob" || len(table.List[0].List) != 2 {
		t.Fatalf("table = %s", table.Display())
	}
	back := call(t, c, "records", Args{"rows": table, "columns": names("id", "customer")})
	if name, _ := back.List[0].Field("customer"); name.Text != "Ana" {
		t.Errorf("records = %s", back.Display())
	}
	if _, ok := back.List[0].Field("extra"); ok {
		t.Error("a column that was not named came back")
	}
}

func TestAProblemInRowsSaysTheRowAndNeverTheContent(t *testing.T) {
	c := codec(t, 0)
	secret := text("Ana Rivers 123.456.789-00")
	rows := value.List([]value.Value{value.Record(map[string]value.Value{"id": secret})})
	_, err := c.Call(context.Background(), "table", Args{"rows": rows, "columns": names("id", "missing")})
	shown := rendered(t, err)
	if !strings.Contains(shown, "row 1 has no field called `missing`") || strings.Contains(shown, "Rivers") {
		t.Errorf("shown = %s", shown)
	}
	fail(t, c, "table", Args{"rows": text("no"), "columns": names("a")}, "needs `rows` to be a list")
	fail(t, c, "table", Args{"rows": value.List([]value.Value{text("no")}), "columns": names("a")}, "row 1 is")
	fail(t, c, "table", Args{"rows": value.List(nil), "columns": value.List(nil)}, "list of names")
	fail(t, c, "table", Args{"rows": value.List(nil), "columns": names("a", "a")}, "different names")
	fail(t, c, "records", Args{"rows": value.List([]value.Value{value.List([]value.Value{text("x")})}), "columns": names("a", "b")}, "row 1 has to be a list of 2 values")
}

func TestJSONIsWrittenAndReadBack(t *testing.T) {
	c := codec(t, 0)
	record := value.Record(map[string]value.Value{
		"n": value.Number(3), "pi": value.Number(2.5), "t": text("<a & b> \"q\""), "ok": value.Bool(true),
		"nothing": value.Nothing, "list": value.List([]value.Value{value.Number(1), text("x")}),
	})
	written := call(t, c, "json", Args{"value": record})
	for _, want := range []string{`"n":3`, `"pi":2.5`, `"t":"<a & b> \"q\""`, `"ok":true`, `"nothing":null`, `"list":[1,"x"]`} {
		if !strings.Contains(written.Text, want) {
			t.Errorf("json = %s, missing %s", written.Text, want)
		}
	}
	back := call(t, c, "parse", Args{"text": written})
	if !value.Equal(back, record) {
		t.Errorf("parse = %s, want %s", back.Display(), record.Display())
	}
	if size := call(t, c, "size", Args{"value": record}); size.Number != float64(len(written.Text)) {
		t.Errorf("size = %v, want %d", size.Number, len(written.Text))
	}
}

func TestTextThatIsNotJSONIsRefusedWithoutRepeatingIt(t *testing.T) {
	c := codec(t, 0)
	_, err := c.Call(context.Background(), "parse", Args{"text": text(`{"name": "Ana Rivers", oops`)})
	shown := rendered(t, err)
	if !strings.Contains(shown, "could not read the text as JSON") || strings.Contains(shown, "Rivers") {
		t.Errorf("shown = %s", shown)
	}
	fail(t, c, "parse", Args{"text": text(`{"a":1`)}, "could not read")
	fail(t, c, "parse", Args{"text": text(`1 2`)}, "more after the first JSON value")
	fail(t, c, "parse", Args{"text": value.Number(1)}, "needs a text")
	fail(t, c, "parse", Args{}, "needs a text")
}

func TestGzipPacksAndUnpacksAndTheSHAIsStable(t *testing.T) {
	c := codec(t, 0)
	body := text(strings.Repeat(`[1,"Ana",10.5],`, 500))
	packed := call(t, c, "gzip", Args{"text": body})
	if len(packed.Text) >= len(body.Text)/4 {
		t.Errorf("packed %d bytes into %d", len(body.Text), len(packed.Text))
	}
	if back := call(t, c, "gunzip", Args{"text": packed}); back.Text != body.Text {
		t.Error("gunzip did not give back the text")
	}
	// The SHA-256 of "abc" is a known value.
	if sum := call(t, c, "sha256", Args{"text": text("abc")}); sum.Text != "ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad" {
		t.Errorf("sha256 = %s", sum.Text)
	}
}

func TestAPackedTextIsLimitedOnceUnpacked(t *testing.T) {
	big := codec(t, 0)
	bomb := call(t, big, "gzip", Args{"text": text(strings.Repeat("a", 200_000))})
	if len(bomb.Text) > 2000 {
		t.Fatalf("the test text is not small: %d", len(bomb.Text))
	}
	small := codec(t, 1000)
	fail(t, small, "gunzip", Args{"text": bomb}, "passes the 1000 bytes")
	fail(t, small, "gzip", Args{"text": text(strings.Repeat("a", 2000))}, "passes the 1000 bytes")
	fail(t, small, "sha256", Args{"text": text(strings.Repeat("a", 2000))}, "passes the 1000 bytes")
	fail(t, small, "json", Args{"value": text(strings.Repeat("a", 2000))}, "passes the 1000 bytes")
	fail(t, small, "parse", Args{"text": text(strings.Repeat("1", 2000))}, "passes the 1000 bytes")
}

func TestDamagedPackedTextIsRefused(t *testing.T) {
	c := codec(t, 0)
	fail(t, c, "gunzip", Args{"text": text("not base64 !!")}, "could not read the text as base64")
	fail(t, c, "gunzip", Args{"text": text("aGVsbG8=")}, "could not read the text as gzip")
	good := call(t, c, "gzip", Args{"text": text(strings.Repeat("hello ", 100))}).Text
	fail(t, c, "gunzip", Args{"text": text(good[:len(good)-12])}, "it is damaged")
}

var uuidV7 = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-7[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)

func TestAUUIDIsVersion7AndCarriesTheTime(t *testing.T) {
	c := codec(t, 0)
	before := time.Now().UnixMilli()
	first := call(t, c, "uuid", Args{}).Text
	second := call(t, c, "uuid", Args{}).Text
	if !uuidV7.MatchString(first) || !uuidV7.MatchString(second) || first == second {
		t.Fatalf("uuids = %s, %s", first, second)
	}
	hex := strings.ReplaceAll(first[:13], "-", "")
	var ms int64
	for _, ch := range hex {
		ms = ms*16 + int64(strings.IndexRune("0123456789abcdef", ch))
	}
	if ms < before || ms > time.Now().UnixMilli() {
		t.Errorf("time in the uuid = %d, now between %d and %d", ms, before, time.Now().UnixMilli())
	}
}

func TestACodecKnowsItsActionsAndSaysSoForAnotherOne(t *testing.T) {
	c := codec(t, 0)
	actions, _ := c.Actions(context.Background())
	if len(actions) != 10 {
		t.Errorf("actions = %d", len(actions))
	}
	fail(t, c, "jsno", Args{}, "has no action called `jsno`")
	if c.Name() != "codec" {
		t.Errorf("name = %s", c.Name())
	}
}
