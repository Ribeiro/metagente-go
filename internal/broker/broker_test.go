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

func fetchOne(t *testing.T, c Consumer) Delivery {
	t.Helper()
	got, err := c.Fetch(context.Background(), 1, 20*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		return nil
	}
	return got[0]
}

func TestAConsumerGetsTheMessagesOfItsSubjectAndAConfirmedOneDoesNotComeAgain(t *testing.T) {
	m := NewMemory()
	ctx := context.Background()
	for _, subject := range []string{"etl.a.batch", "etl.a.control", "etl.b.batch"} {
		if _, err := m.Publish(ctx, Message{Subject: subject, Data: []byte(subject)}); err != nil {
			t.Fatal(err)
		}
	}
	c, err := m.Consume(ctx, ConsumerSpec{Durable: "w", Subject: "etl.*.batch", AckWait: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	first := fetchOne(t, c)
	if first == nil || first.Message().Subject != "etl.a.batch" || first.Attempt() != 1 || first.Seq() != 1 {
		t.Fatalf("first = %+v", first)
	}
	if err := first.Ack(ctx); err != nil {
		t.Fatal(err)
	}
	second := fetchOne(t, c)
	if second == nil || second.Message().Subject != "etl.b.batch" {
		t.Fatalf("second = %+v", second)
	}
	_ = second.Ack(ctx)
	if third := fetchOne(t, c); third != nil {
		t.Errorf("a message of another subject, or a confirmed one: %+v", third.Message())
	}
	// Another consumer with the same name remembers the same things.
	other, _ := m.Consume(ctx, ConsumerSpec{Durable: "w", Subject: "etl.*.batch"})
	if got := fetchOne(t, other); got != nil {
		t.Errorf("a consumer with the same name got %+v", got.Message())
	}
}

func TestAMessageThatIsNotConfirmedComesAgainAfterTheWaitAndAGivenBackOneAfterItsDelay(t *testing.T) {
	m := NewMemory()
	ctx := context.Background()
	_, _ = m.Publish(ctx, Message{Subject: "a.b", Data: []byte("x")})
	c, _ := m.Consume(ctx, ConsumerSpec{Durable: "w", Subject: "a.b", AckWait: time.Minute})
	d := fetchOne(t, c)
	if d == nil {
		t.Fatal("no message")
	}
	if fetchOne(t, c) != nil {
		t.Error("a message that is being worked on was given again")
	}
	m.Advance(2 * time.Minute) // the wait of the confirmation ends
	d = fetchOne(t, c)
	if d == nil || d.Attempt() != 2 {
		t.Fatalf("after the wait: %+v", d)
	}
	if err := d.InProgress(ctx); err != nil {
		t.Fatal(err)
	}
	m.Advance(30 * time.Second)
	if fetchOne(t, c) != nil {
		t.Error("the work in progress was not respected")
	}
	if err := d.Nak(ctx, time.Hour); err != nil {
		t.Fatal(err)
	}
	if fetchOne(t, c) != nil {
		t.Error("a message given back with a delay came at once")
	}
	m.Advance(2 * time.Hour)
	if d = fetchOne(t, c); d == nil || d.Attempt() != 3 {
		t.Fatalf("after the delay: %+v", d)
	}
	if err := d.Term(ctx, "never"); err != nil {
		t.Fatal(err)
	}
	m.Advance(24 * time.Hour)
	if fetchOne(t, c) != nil {
		t.Error("an ended message came again")
	}
}

func TestTheBrokerStopsDeliveringAfterTheMostDeliveriesAndKeepsTheMessage(t *testing.T) {
	m := NewMemory()
	ctx := context.Background()
	_, _ = m.Publish(ctx, Message{Subject: "a.b", Data: []byte("x")})
	c, _ := m.Consume(ctx, ConsumerSpec{Durable: "w", Subject: "a.b", MaxDeliver: 2})
	for i := 1; i <= 2; i++ {
		d := fetchOne(t, c)
		if d == nil || d.Attempt() != i {
			t.Fatalf("delivery %d: %+v", i, d)
		}
		_ = d.Nak(ctx, 0)
	}
	if fetchOne(t, c) != nil {
		t.Error("a third delivery")
	}
	if len(m.Messages()) != 1 {
		t.Error("the message was thrown away")
	}
}

func TestAConsumerCannotBeMadeOnABrokerThatIsDownOrOnASubjectThatIsNotOne(t *testing.T) {
	m := NewMemory()
	if _, err := m.Consume(context.Background(), ConsumerSpec{Durable: "w", Subject: "a..b"}); !errors.Is(err, ErrRefused) {
		t.Errorf("a bad subject: %v", err)
	}
	m.SetDown(true)
	if _, err := m.Consume(context.Background(), ConsumerSpec{Durable: "w", Subject: "a.b"}); !errors.Is(err, ErrUnavailable) {
		t.Errorf("down: %v", err)
	}
	m.SetDown(false)
	c, _ := m.Consume(context.Background(), ConsumerSpec{Durable: "w", Subject: "a.b"})
	_, _ = m.Publish(context.Background(), Message{Subject: "a.b", Data: []byte("x")})
	d := fetchOne(t, c)
	m.SetDown(true)
	if err := d.Ack(context.Background()); !errors.Is(err, ErrUnavailable) {
		t.Errorf("ack while down: %v", err)
	}
	if _, err := c.Fetch(context.Background(), 1, time.Millisecond); !errors.Is(err, ErrUnavailable) {
		t.Errorf("fetch while down: %v", err)
	}
}

func TestHeadersTravelWithAMessage(t *testing.T) {
	m := NewMemory()
	headers := map[string]string{"Reason": "because"}
	_, _ = m.Publish(context.Background(), Message{Subject: "a.b", Headers: headers})
	headers["Reason"] = "changed"
	if got := m.Messages()[0].Headers["Reason"]; got != "because" {
		t.Errorf("header = %q", got)
	}
}
