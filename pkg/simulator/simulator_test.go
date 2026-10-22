package simulator

import (
	"context"
	"testing"

	"x-client/pkg/store"
)

func TestBuildTxNamespaces_ClassifiesReadsAndWrites(t *testing.T) {
	mem := store.NewInMemoryStore()
	mem.Put("ns", "read-only", []byte("a"))
	mem.Put("ns", "updated", []byte("b"))
	mem.Put("ns", "updated", []byte("b2")) // version 1
	mem.Put("ns", "removed", []byte("c"))
	mem.Put("ns", "gone", []byte("d"))
	mem.Put("ns", "gone", nil) // deleted row

	sim := NewMultiNamespaceSimulator(context.Background(), mem)
	mustGet(t, sim, "ns", "read-only")
	mustGet(t, sim, "ns", "updated")
	mustGet(t, sim, "ns", "removed")
	mustGet(t, sim, "ns", "absent")
	if v, _ := sim.Get("ns", "gone"); v != nil {
		t.Fatalf("deleted key returned %q", v)
	}
	_ = sim.Put("ns", "updated", []byte("new"))
	_ = sim.Del("ns", "removed")
	_ = sim.Put("ns", "blind", []byte("x"))
	_ = sim.Put("ns", "gone", []byte("revived"))

	if v, _ := sim.Get("ns", "updated"); string(v) != "new" {
		t.Fatalf("own write not visible: %q", v)
	}

	nss, err := sim.BuildTxNamespaces()
	if err != nil {
		t.Fatal(err)
	}
	if len(nss) != 1 {
		t.Fatalf("namespaces: %d", len(nss))
	}
	ns := nss[0]

	if len(ns.ReadsOnly) != 2 || string(ns.ReadsOnly[0].Key) != "absent" || ns.ReadsOnly[0].Version != nil ||
		string(ns.ReadsOnly[1].Key) != "read-only" || *ns.ReadsOnly[1].Version != 0 {
		t.Fatalf("reads only: %v", ns.ReadsOnly)
	}
	if len(ns.ReadWrites) != 2 ||
		string(ns.ReadWrites[0].Key) != "removed" || ns.ReadWrites[0].Value != nil ||
		string(ns.ReadWrites[1].Key) != "updated" || *ns.ReadWrites[1].Version != 1 || string(ns.ReadWrites[1].Value) != "new" {
		t.Fatalf("read writes: %v", ns.ReadWrites)
	}
	// A deleted row is not a read, so rewriting it is a blind write (as in fabric-x-sdk).
	if len(ns.BlindWrites) != 2 || string(ns.BlindWrites[0].Key) != "blind" || string(ns.BlindWrites[1].Key) != "gone" {
		t.Fatalf("blind writes: %v", ns.BlindWrites)
	}
}

func TestListPrefix_MergesPendingWrites(t *testing.T) {
	mem := store.NewInMemoryStore()
	mem.Put("ns", "P~1", []byte("1"))
	mem.Put("ns", "P~2", []byte("2"))
	mem.Put("ns", "Q~1", []byte("q"))

	sim := NewMultiNamespaceSimulator(context.Background(), mem)
	_ = sim.Del("ns", "P~1")
	_ = sim.Put("ns", "P~3", []byte("3"))

	got, err := sim.ListPrefix("ns", "P~")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || string(got["P~2"]) != "2" || string(got["P~3"]) != "3" {
		t.Fatalf("prefix scan: %v", got)
	}
}

func mustGet(t *testing.T, sim *MultiNamespaceSimulator, ns, key string) {
	t.Helper()
	if _, err := sim.Get(ns, key); err != nil {
		t.Fatal(err)
	}
}
