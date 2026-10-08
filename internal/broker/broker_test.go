package broker

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestSubjectsAndPatternsAreWrittenWithNamesAndDots(t *testing.T) {
	for text, want := range map[string][2]bool{ // {subject, pattern}
		"etl.orders.batch": {true, true},
		"etl":              {true, true},
		"a-b.c_d.9":        {true, true},
		"etl.*.batch":      {false, true},
		"etl.>":            {false, true},
		">":                {false, true},
		"etl.>.batch":      {false, false},
		"etl..batch":       {false, false},
		".etl":             {false, false},
		"etl.":             {false, false},
		"":                 {false, false},
		"etl.or ders":      {false, false},
		"etl.or*ders":      {false, false},
		"etl.ordérs":       {false, false},
		"etl.a>":           {false, false},
	} {
		if got := ValidSubject(text); got != want[0] {
			t.Errorf("ValidSubject(%q) = %v", text, got)
		}
		if got := ValidPattern(text); got != want[1] {
			t.Errorf("ValidPattern(%q) = %v", text, got)
		}
	}
	long := make([]byte, 256)
	for i := range long {
		long[i] = 'a'
	}
	if ValidSubject(string(long)) {
		t.Error("a subject of 256 bytes")
	}
}

func TestAPatternAllowsTheSubjectsThatItNames(t *testing.T) {
	for _, c := range []struct {
		pattern, subject string
		want             bool
	}{
		{"etl.orders.batch", "etl.orders.batch", true},
		{"etl.orders.batch", "etl.orders.control", false},
		{"etl.*.batch", "etl.orders.batch", true},
		{"etl.*.batch", "etl.orders.x.batch", false},
		{"etl.*", "etl.orders", true},
		{"etl.*", "etl", false},
		{"etl.>", "etl.orders.batch", true},
		{"etl.>", "etl.orders", true},
		{"etl.>", "etl", false},
		{"etl.>", "other.orders", false},
		{">", "anything.at.all", true},
		{"etl.orders", "etl.orders.batch", false},
		{"etl.orders.batch", "etl.orders", false},
	} {
		if got := Matches(c.pattern, c.subject); got != c.want {
			t.Errorf("Matches(%q, %q) = %v", c.pattern, c.subject, got)
		}
	}
}

func TestTheMemoryBrokerDropsACopyOfAnIDInsideTheWindow(t *testing.T) {
	m := NewMemory()
	ctx := context.Background()
	first, err := m.Publish(ctx, Message{Subject: "a.b", ID: "job:1", Data: []byte("x")})
	if err != nil || first.Duplicate || first.Seq != 1 {
		t.Fatalf("first = %+v, %v", first, err)
	}
	again, err := m.Publish(ctx, Message{Subject: "a.b", ID: "job:1", Data: []byte("x")})
	if err != nil || !again.Duplicate || again.Seq != 1 || len(m.Messages()) != 1 {
		t.Fatalf("again = %+v, %v, %d messages", again, err, len(m.Messages()))
	}
	if other, _ := m.Publish(ctx, Message{Subject: "a.b", ID: "job:2"}); other.Duplicate || other.Seq != 2 {
		t.Errorf("another id = %+v", other)
	}
	m.Advance(DefaultWindow + time.Second)
	if late, _ := m.Publish(ctx, Message{Subject: "a.b", ID: "job:1"}); late.Duplicate || late.Seq != 3 {
		t.Errorf("after the window = %+v", late)
	}
	if noID, _ := m.Publish(ctx, Message{Subject: "a.b"}); noID.Duplicate {
		t.Errorf("a message with no id was taken for a copy: %+v", noID)
	}
}

func TestTheMemoryBrokerRefusesWhenFullAndWhenDown(t *testing.T) {
	ctx := context.Background()
	full := NewMemory()
	full.MaxMessages = 2
	for i := 0; i < 2; i++ {
		if _, err := full.Publish(ctx, Message{Subject: "a", Data: []byte("x")}); err != nil {
			t.Fatal(err)
		}
	}
	_, err := full.Publish(ctx, Message{Subject: "a", ID: "new"})
	if !errors.Is(err, ErrFull) || !MayPass(err) || len(full.Messages()) != 2 {
		t.Errorf("full: %v", err)
	}
	heavy := NewMemory()
	heavy.MaxBytes = 10
	if _, err := heavy.Publish(ctx, Message{Subject: "a", Data: make([]byte, 8)}); err != nil {
		t.Fatal(err)
	}
	if _, err := heavy.Publish(ctx, Message{Subject: "a", Data: make([]byte, 8)}); !errors.Is(err, ErrFull) {
		t.Errorf("heavy: %v", err)
	}
	down := NewMemory()
	down.SetDown(true)
	if _, err := down.Publish(ctx, Message{Subject: "a"}); !errors.Is(err, ErrUnavailable) || !MayPass(err) {
		t.Errorf("down: %v", err)
	}
	down.SetDown(false)
	if _, err := down.Publish(ctx, Message{Subject: "a"}); err != nil {
		t.Errorf("working again: %v", err)
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := NewMemory().Publish(cancelled, Message{Subject: "a"}); !errors.Is(err, context.Canceled) {
		t.Errorf("cancelled: %v", err)
	}
	if MayPass(ErrRefused) || MayPass(ErrNoStream) {
		t.Error("a refusal that is final may pass")
	}
}

func TestTheMemoryBrokerKeepsACopyOfTheData(t *testing.T) {
	m := NewMemory()
	data := []byte("abc")
	if _, err := m.Publish(context.Background(), Message{Subject: "a", Data: data}); err != nil {
		t.Fatal(err)
	}
	data[0] = 'X'
	if got := string(m.Messages()[0].Data); got != "abc" {
		t.Errorf("data = %q", got)
	}
}
