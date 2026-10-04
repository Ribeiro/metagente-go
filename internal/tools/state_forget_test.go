package tools

import (
	"testing"

	"metagente/internal/config"
	"metagente/internal/value"
)

// req: S7, S9
func TestForgettingAContextDropsItsMemoriesAndNoOtherContexts(t *testing.T) {
	store := NewStateStore(config.Default().Limits)
	_ = store.For("A", "ctx-1").Set("k", value.Text("a1"))
	_ = store.For("B", "ctx-1").Set("k", value.Text("b1"))
	_ = store.For("A", "ctx-2").Set("k", value.Text("a2"))

	store.Forget("ctx-1")

	if got := store.For("A", "ctx-1").Get("k"); got.Kind != value.KindNothing {
		t.Errorf("A in ctx-1 still remembers %v", got.Display())
	}
	if got := store.For("B", "ctx-1").Get("k"); got.Kind != value.KindNothing {
		t.Errorf("B in ctx-1 still remembers %v", got.Display())
	}
	if got := store.For("A", "ctx-2").Get("k"); got.Text != "a2" {
		t.Errorf("another context lost its memory: %v", got.Display())
	}
	store.Forget("never-existed") // nothing to forget is not a problem
}

func TestForgettingDoesNotTouchAContextWhoseNameEndsTheSame(t *testing.T) {
	store := NewStateStore(config.Default().Limits)
	_ = store.For("A", "ctx-1").Set("k", value.Text("short"))
	_ = store.For("A", "other-ctx-1").Set("k", value.Text("long"))
	store.Forget("ctx-1")
	if got := store.For("A", "other-ctx-1").Get("k"); got.Text != "long" {
		t.Errorf("a context was forgotten because its name ends like another: %v", got.Display())
	}
}
