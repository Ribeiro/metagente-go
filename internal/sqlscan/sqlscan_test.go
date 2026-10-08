package sqlscan

import (
	"fmt"
	"reflect"
	"strings"
	"testing"
)

func TestTheParametersAreFoundInTheOrderOfTheirFirstUse(t *testing.T) {
	s, err := Parse("SELECT id, name FROM orders WHERE id > :after AND region = :region AND id <= :after + :size ORDER BY id LIMIT :size;")
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"after", "region", "size"}; !reflect.DeepEqual(s.Params, want) {
		t.Errorf("params = %v, want %v", s.Params, want)
	}
	if strings.HasSuffix(s.Text, ";") || !strings.HasSuffix(s.Text, "LIMIT :size") {
		t.Errorf("text = %q", s.Text)
	}
}

func TestRewriteGivesAPlaceForEachUseOfAParameter(t *testing.T) {
	s, err := Parse("select * from t where a = :x and b = :y and c = :x")
	if err != nil {
		t.Fatal(err)
	}
	query, order := s.Rewrite(func(int) string { return "?" })
	if query != "select * from t where a = ? and b = ? and c = ?" {
		t.Errorf("query = %q", query)
	}
	if want := []string{"x", "y", "x"}; !reflect.DeepEqual(order, want) {
		t.Errorf("order = %v, want %v", order, want)
	}
	numbered, _ := s.Rewrite(func(n int) string { return fmt.Sprintf("$%d", n) })
	if numbered != "select * from t where a = $1 and b = $2 and c = $3" {
		t.Errorf("numbered = %q", numbered)
	}
}

func TestWhatIsNotCodeIsNotAParameter(t *testing.T) {
	for name, text := range map[string]string{
		"in a text":          "select ':a' as x, 'it''s :b' as y",
		"in an identifier":   `select 1 as "a:c", 2 as ` + "`d:e`",
		"in a line comment":  "select 1 -- :f\n",
		"in a block comment": "select 1 /* :g :h */",
		"a cast":             "select x::int, y::text from t",
		"a time":             "select 1 where t > time '12:30'",
		"inside a word":      "select a:b from t",
		"a colon alone":      "select 1 where x = : and y = :1",
	} {
		s, err := Parse(text)
		if err != nil {
			t.Errorf("%s: %v", name, err)
			continue
		}
		if len(s.Params) != 0 {
			t.Errorf("%s: params = %v, want none", name, s.Params)
		}
		if query, _ := s.Rewrite(func(int) string { return "?" }); query != s.Text {
			t.Errorf("%s: the text changed: %q", name, query)
		}
	}
}

func TestParametersNextToSigns(t *testing.T) {
	s, err := Parse("select * from t where a in (:a,:b) and c=:c")
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"a", "b", "c"}; !reflect.DeepEqual(s.Params, want) {
		t.Errorf("params = %v, want %v", s.Params, want)
	}
}

func TestOnlyAReadingStatementIsAccepted(t *testing.T) {
	for _, ok := range []string{"select 1", "SELECT 1", "  \n-- a note\n select 1", "/* x */ with c as (select 1) select * from c", "select 1;", "select 1; -- the end\n"} {
		if _, err := Parse(ok); err != nil {
			t.Errorf("%q: %v", ok, err)
		}
	}
	for text, want := range map[string]string{
		"delete from t":                 "begins with DELETE",
		"update t set a = 1":            "begins with UPDATE",
		"insert into t values (1)":      "begins with INSERT",
		"drop table t":                  "begins with DROP",
		"pragma writable_schema = on":   "begins with PRAGMA",
		"":                              "it is empty",
		"  -- only a note":              "it is empty",
		"select 1; select 2":            "more than one statement",
		"select 1; delete from t":       "more than one statement",
		"select 1;;":                    "more than one statement",
		"select 'a":                     "never closed",
		"select 1 /* a":                 "never closed",
		"select 1; /* a */ select 2":    "more than one statement",
		"select 1 where a = ':b'; x":    "more than one statement",
		"select 1;'text'":               "more than one statement",
		"select 1 ; :a":                 "more than one statement",
		strings.Repeat("a", MaxBytes+1): "longer than",
	} {
		_, err := Parse(text)
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%.30q: error = %v, want it to say %q", text, err, want)
		}
	}
}

func TestASemicolonInsideTextOrACommentDoesNotEndTheStatement(t *testing.T) {
	s, err := Parse("select ';' as a, \"b;c\" as d from t -- ; not the end\n where x = :x")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(s.Params, []string{"x"}) || !strings.HasSuffix(s.Text, "where x = :x") {
		t.Errorf("params = %v, text = %q", s.Params, s.Text)
	}
}

func TestWordsOutsideASCIIAreWordsOfTheStatement(t *testing.T) {
	s, err := Parse("select código from pedidos where cliente = :cliente")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(s.Params, []string{"cliente"}) {
		t.Errorf("params = %v", s.Params)
	}
}

func TestAStatementThatChangesRowsIsReadOnlyWhereItIsAllowed(t *testing.T) {
	for text, kind := range map[string]Kind{
		"select 1":                               Read,
		"with a as (select 1) select * from a":   Read,
		"insert into t (a) values (:a)":          Insert,
		"UPDATE t SET a = :a WHERE id = :id":     Update,
		"delete from t where day < :day":         Delete,
		"update t set a = 'where' where b = :b ": Update,
	} {
		s, err := ParseWrite(text)
		if err != nil || s.Kind != kind || s.Kind.Writes() != (kind != Read) {
			t.Errorf("%q: kind = %v, err = %v", text, s, err)
		}
		if kind.Writes() {
			if _, err := Parse(text); err == nil || !strings.Contains(err.Error(), "begins with") {
				t.Errorf("%q: a connection that only reads must refuse it, got %v", text, err)
			}
		}
	}
	for text, want := range map[string]string{
		"update t set a = 1":                   "UPDATE with no WHERE",
		"delete from t":                        "DELETE with no WHERE",
		"delete from t -- where":               "no WHERE",
		"update t set a = 'where'":             "no WHERE",
		"drop table t":                         "begins with DROP",
		"create table t (a int)":               "begins with CREATE",
		"alter table t add b int":              "begins with ALTER",
		"insert into t values (1); delete t w": "more than one statement",
	} {
		_, err := ParseWrite(text)
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%q: error = %v, want %q", text, err, want)
		}
	}
}
