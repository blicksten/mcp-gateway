package obs

import "testing"

func TestKillRing_PushAndSnapshot_NotFull(t *testing.T) {
	r := NewKillRing(4)
	r.Push(KillEvent{Backend: "a", Pid: 1, Actor: "reaper", Reason: "owner-absent"})
	r.Push(KillEvent{Backend: "b", Pid: 2, Actor: "suture", Reason: "keepalive-miss"})

	if r.Len() != 2 {
		t.Fatalf("Len = %d, want 2", r.Len())
	}
	snap := r.Snapshot()
	if len(snap) != 2 {
		t.Fatalf("Snapshot len = %d, want 2", len(snap))
	}
	if snap[0].Backend != "a" || snap[1].Backend != "b" {
		t.Errorf("order wrong: %q, %q (want a, b)", snap[0].Backend, snap[1].Backend)
	}
	if snap[0].Ts == "" {
		t.Errorf("Ts not auto-stamped on push")
	}
}

func TestKillRing_Eviction(t *testing.T) {
	r := NewKillRing(3)
	// Push 5 into a size-3 ring; oldest two (k0, k1) must be evicted.
	for i := 0; i < 5; i++ {
		r.Push(KillEvent{Backend: string(rune('a' + i)), Pid: i})
	}
	if r.Len() != 3 {
		t.Fatalf("Len = %d, want 3 (capped)", r.Len())
	}
	snap := r.Snapshot()
	if len(snap) != 3 {
		t.Fatalf("Snapshot len = %d, want 3", len(snap))
	}
	// Oldest retained should be the 3rd push ('c'), then 'd', then 'e'.
	wantBackends := []string{"c", "d", "e"}
	for i, want := range wantBackends {
		if snap[i].Backend != want {
			t.Errorf("snap[%d].Backend = %q, want %q", i, snap[i].Backend, want)
		}
	}
}

func TestKillRing_ExactlyFull(t *testing.T) {
	r := NewKillRing(2)
	r.Push(KillEvent{Backend: "x"})
	r.Push(KillEvent{Backend: "y"})
	if r.Len() != 2 {
		t.Fatalf("Len = %d, want 2", r.Len())
	}
	snap := r.Snapshot()
	if snap[0].Backend != "x" || snap[1].Backend != "y" {
		t.Errorf("order wrong: %q, %q", snap[0].Backend, snap[1].Backend)
	}
}

func TestKillRing_DefaultSizeOnNonPositive(t *testing.T) {
	r := NewKillRing(0)
	if r.size != DefaultKillRingSize {
		t.Errorf("size = %d, want default %d", r.size, DefaultKillRingSize)
	}
	r2 := NewKillRing(-5)
	if r2.size != DefaultKillRingSize {
		t.Errorf("size = %d, want default %d", r2.size, DefaultKillRingSize)
	}
}

func TestKillRing_NilSafe(t *testing.T) {
	var r *KillRing
	r.Push(KillEvent{Backend: "z"}) // must not panic
	if r.Len() != 0 {
		t.Errorf("nil ring Len = %d, want 0", r.Len())
	}
	if r.Snapshot() != nil {
		t.Errorf("nil ring Snapshot != nil")
	}
}
