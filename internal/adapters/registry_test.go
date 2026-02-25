package adapters

import "testing"

// stubAdapter is a minimal Adapter for testing the registry.
type stubAdapter struct {
	name string
}

func (s *stubAdapter) Name() string { return s.name }

func TestRegisterAndGet(t *testing.T) {
	Reset()
	defer Reset()

	a := &stubAdapter{name: "test-adapter"}
	Register(a)

	got := Get("test-adapter")
	if got == nil {
		t.Fatal("Get returned nil for registered adapter")
	}
	if got.Name() != "test-adapter" {
		t.Errorf("Name() = %q, want %q", got.Name(), "test-adapter")
	}
}

func TestGetUnregistered(t *testing.T) {
	Reset()
	defer Reset()

	if got := Get("nonexistent"); got != nil {
		t.Errorf("Get returned non-nil for unregistered adapter: %v", got)
	}
}

func TestAll(t *testing.T) {
	Reset()
	defer Reset()

	Register(&stubAdapter{name: "a"})
	Register(&stubAdapter{name: "b"})

	all := All()
	if len(all) != 2 {
		t.Fatalf("All() returned %d adapters, want 2", len(all))
	}
	if all["a"] == nil || all["b"] == nil {
		t.Error("All() missing expected adapters")
	}

	// Verify returned map is a copy (mutating it doesn't affect registry)
	delete(all, "a")
	if Get("a") == nil {
		t.Error("deleting from All() result affected the registry")
	}
}

func TestReset(t *testing.T) {
	Reset()
	Register(&stubAdapter{name: "x"})
	Reset()

	if got := Get("x"); got != nil {
		t.Error("Reset did not clear registry")
	}
}

func TestRegisterOverwrite(t *testing.T) {
	Reset()
	defer Reset()

	a1 := &stubAdapter{name: "dup"}
	a2 := &stubAdapter{name: "dup"}
	Register(a1)
	Register(a2)

	got := Get("dup")
	if got != a2 {
		t.Error("later Register should overwrite earlier one")
	}
}
