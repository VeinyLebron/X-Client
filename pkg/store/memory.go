package store

import (
	"bytes"
	"context"
	"strings"
	"sync"

	"github.com/hyperledger/fabric-x-common/api/applicationpb"
	"github.com/hyperledger/fabric-x-common/api/committerpb"
)

// InMemoryStore is a single-process ledger. Commit applies Fabric-X transactions with
// the same MVCC rules as the committer, so standalone mode behaves like the network.
type InMemoryStore struct {
	mu   sync.RWMutex
	data map[string]map[string]Row // namespace -> key -> row
	txs  map[string]committerpb.Status
}

func NewInMemoryStore() *InMemoryStore {
	return &InMemoryStore{
		data: make(map[string]map[string]Row),
		txs:  make(map[string]committerpb.Status),
	}
}

func (m *InMemoryStore) Close() error { return nil }

// Put writes a value directly, bypassing MVCC. Intended for seeding tests.
func (m *InMemoryStore) Put(namespace, key string, value []byte) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.write(namespace, key, value)
}

// write applies one write under m.mu. An empty value deletes the key.
func (m *InMemoryStore) write(namespace, key string, value []byte) {
	ns, ok := m.data[namespace]
	if !ok {
		ns = make(map[string]Row)
		m.data[namespace] = ns
	}
	row, exists := ns[key]
	next := Row{Value: bytes.Clone(value), Deleted: len(value) == 0}
	if exists {
		next.Version = row.Version + 1
	}
	ns[key] = next
}

func (m *InMemoryStore) GetRows(_ context.Context, namespace string, keys []string) (map[string]Row, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	out := make(map[string]Row)
	for _, k := range keys {
		if row, ok := m.data[namespace][k]; ok {
			row.Value = bytes.Clone(row.Value)
			out[k] = row
		}
	}
	return out, nil
}

func (m *InMemoryStore) ListPrefix(_ context.Context, namespace, prefix string) (map[string]Row, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	out := make(map[string]Row)
	for k, row := range m.data[namespace] {
		if strings.HasPrefix(k, prefix) && !row.Deleted {
			row.Value = bytes.Clone(row.Value)
			out[k] = row
		}
	}
	return out, nil
}

// NamespaceVersion is always 0: namespaces are never updated in standalone mode.
func (m *InMemoryStore) NamespaceVersion(context.Context, string) (uint64, error) {
	return 0, nil
}

// Commit validates tx against the current state and applies it atomically.
// Endorsement policies are not checked here; the engine only submits fully endorsed txs.
func (m *InMemoryStore) Commit(txID string, tx *applicationpb.Tx) committerpb.Status {
	m.mu.Lock()
	defer m.mu.Unlock()

	if _, seen := m.txs[txID]; seen {
		return committerpb.Status_REJECTED_DUPLICATE_TX_ID
	}
	status := m.validate(tx)
	m.txs[txID] = status
	if status != committerpb.Status_COMMITTED {
		return status
	}

	for _, ns := range tx.Namespaces {
		for _, rw := range ns.ReadWrites {
			m.write(ns.NsId, string(rw.Key), rw.Value)
		}
		for _, w := range ns.BlindWrites {
			m.write(ns.NsId, string(w.Key), w.Value)
		}
	}
	return committerpb.Status_COMMITTED
}

// TxStatus returns the recorded status of a committed or aborted transaction.
func (m *InMemoryStore) TxStatus(txID string) (committerpb.Status, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	s, ok := m.txs[txID]
	return s, ok
}

func (m *InMemoryStore) validate(tx *applicationpb.Tx) committerpb.Status {
	if len(tx.Namespaces) == 0 {
		return committerpb.Status_MALFORMED_EMPTY_NAMESPACES
	}
	if len(tx.Endorsements) != len(tx.Namespaces) {
		return committerpb.Status_MALFORMED_MISSING_SIGNATURE
	}
	for _, ns := range tx.Namespaces {
		if len(ns.ReadWrites) == 0 && len(ns.BlindWrites) == 0 {
			return committerpb.Status_MALFORMED_NO_WRITES
		}
		for _, r := range ns.ReadsOnly {
			if !m.versionMatches(ns.NsId, string(r.Key), r.Version) {
				return committerpb.Status_ABORTED_MVCC_CONFLICT
			}
		}
		for _, rw := range ns.ReadWrites {
			if !m.versionMatches(ns.NsId, string(rw.Key), rw.Version) {
				return committerpb.Status_ABORTED_MVCC_CONFLICT
			}
		}
	}
	return committerpb.Status_COMMITTED
}

// versionMatches reports whether a read at version ver is still current.
// A nil version asserts the key does not exist (or was deleted).
func (m *InMemoryStore) versionMatches(namespace, key string, ver *uint64) bool {
	row, ok := m.data[namespace][key]
	if ver == nil {
		return !ok || row.Deleted
	}
	return ok && !row.Deleted && row.Version == *ver
}
