package value

import (
	"encoding/json"
	"testing"
)

func TestDisplay(t *testing.T) {
	tests := []struct {
		name string
		v    Value
		want string
	}{
		{"nothing", Nothing, "nothing"},
		{"yes", Bool(true), "yes"},
		{"no", Bool(false), "no"},
		{"whole number", Number(3), "3"},
		{"fraction", Number(2.5), "2.5"},
		{"negative whole", Number(-7), "-7"},
		{"text", Text("hi"), "hi"},
		{"list", List([]Value{Number(1), Text("a"), Bool(true)}), "[1, a, yes]"},
		{"empty list", List(nil), "[]"},
		{"record is sorted by field", Record(map[string]Value{"b": Number(2), "a": Text("x")}), "{a: x, b: 2}"},
	}
	for _, tt := range tests {
		if got := tt.v.Display(); got != tt.want {
			t.Errorf("%s: Display() = %q, want %q", tt.name, got, tt.want)
		}
	}
}

func TestTruthy(t *testing.T) {
	falsy := []Value{Nothing, Bool(false), Number(0), Text(""), List(nil), Record(nil)}
	for _, v := range falsy {
		if v.Truthy() {
			t.Errorf("%s should be false", v.Describe())
		}
	}
	truthy := []Value{Bool(true), Number(1), Number(-1), Text("a"), List([]Value{Nothing}), Record(map[string]Value{"a": Nothing})}
	for _, v := range truthy {
		if !v.Truthy() {
			t.Errorf("%v should be true", v.Display())
		}
	}
}

func TestAsNumberReadsTextThatLooksLikeANumber(t *testing.T) {
	if n, ok := Text(" 5 ").AsNumber(); !ok || n != 5 {
		t.Errorf("AsNumber(\" 5 \") = %v, %v", n, ok)
	}
	if n, ok := Number(2.5).AsNumber(); !ok || n != 2.5 {
		t.Errorf("AsNumber(2.5) = %v, %v", n, ok)
	}
	for _, v := range []Value{Text("abc"), Nothing, Bool(true), List(nil)} {
		if _, ok := v.AsNumber(); ok {
			t.Errorf("%s should not be a number", v.Describe())
		}
	}
}

func TestFieldAndFieldNames(t *testing.T) {
	r := Record(map[string]Value{"unix": Number(1), "text": Text("now")})
	if f, ok := r.Field("text"); !ok || f.Text != "now" {
		t.Errorf("Field(text) = %v, %v", f, ok)
	}
	if _, ok := r.Field("nope"); ok {
		t.Error("Field(nope) should not exist")
	}
	if _, ok := Text("x").Field("a"); ok {
		t.Error("only records have fields")
	}
	names := r.FieldNames()
	if len(names) != 2 || names[0] != "text" || names[1] != "unix" {
		t.Errorf("FieldNames() = %v", names)
	}
}

func TestEqual(t *testing.T) {
	a := List([]Value{Number(1), Record(map[string]Value{"k": Text("v")})})
	b := List([]Value{Number(1), Record(map[string]Value{"k": Text("v")})})
	c := List([]Value{Number(1), Record(map[string]Value{"k": Text("w")})})
	if !Equal(a, b) {
		t.Error("equal values reported as different")
	}
	if Equal(a, c) {
		t.Error("different values reported as equal")
	}
	if Equal(Number(1), Text("1")) {
		t.Error("a number and a text are different values for Equal")
	}
	if !Equal(Nothing, Nothing) {
		t.Error("nothing equals nothing")
	}
}

func TestJSONRoundTrip(t *testing.T) {
	text := `{"a":[1,2.5,"x",true,null],"b":{"c":"d"}}`
	var j any
	if err := json.Unmarshal([]byte(text), &j); err != nil {
		t.Fatal(err)
	}
	v := FromJSON(j)
	if v.Kind != KindRecord {
		t.Fatalf("kind = %v", v.Kind)
	}
	out, err := json.Marshal(v.ToJSON())
	if err != nil {
		t.Fatal(err)
	}
	if string(out) != text {
		t.Errorf("round trip = %s, want %s", out, text)
	}
}

func TestWholeNumbersBecomeIntegersInJSON(t *testing.T) {
	out, _ := json.Marshal(Number(3).ToJSON())
	if string(out) != "3" {
		t.Errorf("3 became %s", out)
	}
}
