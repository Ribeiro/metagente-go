package broker

import (
	"context"
	"testing"
	"time"
)

func TestAMessageTheBrokerLostIsNeverGivenAndItsIdCanBeUsedAgainAfterTheWindow(t *testing.T) {
	ctx := context.Background()
	m := NewMemory()
	for _, id := range []string{"j:1", "j:2", "j:3"} {
		if _, err := m.Publish(ctx, Message{Subject: "etl.j.batch", ID: id, Data: []byte(id)}); err != nil {
			t.Fatal(err)
		}
	}
	if n := m.Lose("j:2"); n != 1 {
		t.Fatalf("lost %d", n)
	}
	if n := m.Lose("j:2"); n != 0 {
		t.Errorf("a message that is lost was lost again: %d", n)
	}
	c, err := m.Consume(ctx, ConsumerSpec{Durable: "w", Subject: "etl.*.batch", AckWait: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	got, err := c.Fetch(ctx, 10, 20*time.Millisecond)
	if err != nil || len(got) != 2 || string(got[0].Message().Data) != "j:1" || string(got[1].Message().Data) != "j:3" {
		t.Fatalf("given: %v, %v", got, err)
	}
	for _, d := range got {
		if err := d.Ack(ctx); err != nil {
			t.Fatal(err)
		}
	}
	// Inside the window the broker still remembers the id, so a copy is dropped; after it, the id is free.
	if ack, _ := m.Publish(ctx, Message{Subject: "etl.j.batch", ID: "j:2", Data: []byte("again")}); !ack.Duplicate {
		t.Error("the id was forgotten inside the window")
	}
	m.Advance(10 * time.Minute)
	if ack, err := m.Publish(ctx, Message{Subject: "etl.j.batch", ID: "j:2", Data: []byte("again")}); err != nil || ack.Duplicate {
		t.Fatalf("after the window: %+v, %v", ack, err)
	}
	more, _ := c.Fetch(ctx, 10, 20*time.Millisecond)
	if len(more) != 1 || string(more[0].Message().Data) != "again" {
		t.Errorf("the new message was not given: %v", more)
	}
}
