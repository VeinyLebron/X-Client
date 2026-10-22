package store

import (
	"context"
	"testing"

	"github.com/hyperledger/fabric-x-common/api/applicationpb"
	"github.com/hyperledger/fabric-x-common/api/committerpb"
	"github.com/hyperledger/fabric-x-sdk/blocks"
)

func ver(v uint64) *uint64 { return &v }

func oneNs(ns *applicationpb.TxNamespace) *applicationpb.Tx {
	return &applicationpb.Tx{Namespaces: []*applicationpb.TxNamespace{ns}, Endorsements: []*applicationpb.Endorsements{{}}}
}

func TestInMemoryCommit_MVCC(t *testing.T) {
	mem := NewInMemoryStore()
	mem.Put("ns", "k", []byte("v0"))

	stale := oneNs(&applicationpb.TxNamespace{NsId: "ns", ReadWrites: []*applicationpb.ReadWrite{{Key: []byte("k"), Version: ver(0), Value: []byte("v1")}}})
	if s := mem.Commit("tx1", stale); s != committerpb.Status_COMMITTED {
		t.Fatalf("first update: %s", s)
	}
	// Same read version again: k is now at version 1.
	again := oneNs(&applicationpb.TxNamespace{NsId: "ns", ReadWrites: []*applicationpb.ReadWrite{{Key: []byte("k"), Version: ver(0), Value: []byte("v2")}}})
	if s := mem.Commit("tx2", again); s != committerpb.Status_ABORTED_MVCC_CONFLICT {
		t.Fatalf("stale update: %s", s)
	}
	if s := mem.Commit("tx1", stale); s != committerpb.Status_REJECTED_DUPLICATE_TX_ID {
		t.Fatalf("duplicate: %s", s)
	}

	// "Must not exist" read fails once the key exists.
	create := oneNs(&applicationpb.TxNamespace{NsId: "ns", ReadsOnly: []*applicationpb.Read{{Key: []byte("k")}}, BlindWrites: []*applicationpb.Write{{Key: []byte("x"), Value: []byte("1")}}})
	if s := mem.Commit("tx3", create); s != committerpb.Status_ABORTED_MVCC_CONFLICT {
		t.Fatalf("exists check: %s", s)
	}

	rows, _ := mem.GetRows(context.Background(), "ns", []string{"k"})
	if string(rows["k"].Value) != "v1" || rows["k"].Version != 1 {
		t.Fatalf("state: %+v", rows["k"])
	}

	del := oneNs(&applicationpb.TxNamespace{NsId: "ns", ReadWrites: []*applicationpb.ReadWrite{{Key: []byte("k"), Version: ver(1)}}})
	if s := mem.Commit("tx4", del); s != committerpb.Status_COMMITTED {
		t.Fatalf("delete: %s", s)
	}
	if live, _ := mem.ListPrefix(context.Background(), "ns", ""); len(live) != 0 {
		t.Fatalf("deleted key still listed: %v", live)
	}
}

func TestKeyIndex_TracksCommittedWrites(t *testing.T) {
	ix := NewKeyIndex()
	_ = ix.Handle(context.Background(), blocks.Block{Number: 3, Transactions: []blocks.Transaction{
		{Valid: true, NsRWS: []blocks.NsReadWriteSet{{Namespace: "ns", RWS: blocks.ReadWriteSet{Writes: []blocks.KVWrite{
			{Key: "C~WA~1", Value: []byte("a")}, {Key: "C~WA~2", Value: []byte("b")}, {Key: "C~WB~1", Value: []byte("c")},
		}}}}},
		{Valid: false, NsRWS: []blocks.NsReadWriteSet{{Namespace: "ns", RWS: blocks.ReadWriteSet{Writes: []blocks.KVWrite{
			{Key: "C~WA~9", Value: []byte("invalid tx")},
		}}}}},
	}})
	_ = ix.Handle(context.Background(), blocks.Block{Number: 4, Transactions: []blocks.Transaction{
		{Valid: true, NsRWS: []blocks.NsReadWriteSet{{Namespace: "ns", RWS: blocks.ReadWriteSet{Writes: []blocks.KVWrite{
			{Key: "C~WA~1"}, // delete
		}}}}},
	}})

	if got := ix.Keys("ns", "C~WA~"); len(got) != 1 || got[0] != "C~WA~2" {
		t.Fatalf("keys: %v", got)
	}
	if n, _ := ix.BlockNumber(context.Background()); n != 4 {
		t.Fatalf("block number %d", n)
	}
}
