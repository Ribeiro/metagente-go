package diag

import (
	"fmt"
	"strings"
	"testing"
)

func TestRenderShowsPlaceLineMarkerAndFix(t *testing.T) {
	source := "agent A\n  gaol \"x\"\n"
	d := New("I do not know the word `gaol` here").
		At("a.ag", 2, 3).
		WithSource(source).
		Fix("did you mean `goal`?")

	want := "Problem on line 2 of a.ag: I do not know the word `gaol` here\n" +
		"  2 |   gaol \"x\"\n" +
		"    |   ^\n" +
		"Fix: did you mean `goal`?\n"
	if got := d.Render(); got != want {
		t.Errorf("Render() =\n%s\nwant\n%s", got, want)
	}
}

func TestRenderWithoutPlace(t *testing.T) {
	d := New("this file has no agent in it").Fix("start with a line like: agent Helper")
	want := "Problem: this file has no agent in it\nFix: start with a line like: agent Helper\n"
	if got := d.Render(); got != want {
		t.Errorf("Render() = %q, want %q", got, want)
	}
}

func TestRenderUsesYourFileWhenNameIsUnknown(t *testing.T) {
	got := New("boom").At("", 4, 1).Render()
	if !strings.HasPrefix(got, "Problem on line 4 of your file: boom\n") {
		t.Errorf("unexpected rendering: %q", got)
	}
}

func TestRenderOfAWarning(t *testing.T) {
	got := New("careful").At("a.ag", 1, 1).AsWarning().Render()
	if !strings.HasPrefix(got, "Warning on line 1 of a.ag: careful\n") {
		t.Errorf("unexpected rendering: %q", got)
	}
}

func TestRelatedLinesAreIndented(t *testing.T) {
	got := New("outer").AddRelated("inner line one").AddRelated("inner line two").Render()
	want := "Problem: outer\n  inner line one\n  inner line two\n"
	if got != want {
		t.Errorf("Render() = %q, want %q", got, want)
	}
}

// req: P1
func TestPublicKeepsOnlyMessageAndFix(t *testing.T) {
	d := New("the file `notes.txt` does not exist").
		At("secret-agent.ag", 6, 5).
		WithSource("agent A\n  goal \"x\"\n").
		AddRelated("looked in /home/someone/project").
		Fix("check the spelling")

	got := d.Public()
	want := "the file `notes.txt` does not exist\nFix: check the spelling\n"
	if got != want {
		t.Errorf("Public() = %q, want %q", got, want)
	}
	for _, leak := range []string{"secret-agent.ag", "line 6", "/home/someone", "|"} {
		if strings.Contains(got, leak) {
			t.Errorf("Public() leaks %q: %q", leak, got)
		}
	}
}

func TestLocatedOnlyFillsAnUnknownPlace(t *testing.T) {
	d := New("x").Located("a.ag", 3, 2, "source")
	if d.File != "a.ag" || d.Line != 3 || d.Column != 2 || d.Source != "source" {
		t.Fatalf("Located did not fill the place: %+v", d)
	}
	d.Located("b.ag", 9, 9, "other")
	if d.File != "a.ag" || d.Line != 3 || d.Column != 2 || d.Source != "source" {
		t.Errorf("Located overwrote a known place: %+v", d)
	}
}

func TestSortByPlaceKeepsTheOrderOfEqualPlaces(t *testing.T) {
	list := []*Diagnostic{
		New("late").At("a.ag", 9, 1),
		New("first at 2:5").At("a.ag", 2, 5),
		New("second at 2:5").At("a.ag", 2, 5),
		New("early").At("a.ag", 2, 1),
	}
	SortByPlace(list)
	var got []string
	for _, d := range list {
		got = append(got, d.Message)
	}
	want := []string{"early", "first at 2:5", "second at 2:5", "late"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("order = %v, want %v", got, want)
	}
}

func TestFromFindsADiagnosticInsideAWrappedError(t *testing.T) {
	d := New("inner")
	err := fmt.Errorf("while loading: %w", d)
	found, ok := From(err)
	if !ok || found != d {
		t.Errorf("From() = %v, %v; want the original diagnostic", found, ok)
	}
	if _, ok := From(fmt.Errorf("plain")); ok {
		t.Error("From() found a diagnostic in a plain error")
	}
}
