// Package store reads committed world state, either from a Fabric-X Query Service
// or from an in-memory ledger used for standalone runs and tests.
package store

import "context"

// Row is a committed state row. Fabric-X versions are monotonic per key; the first
// write of a key has version 0. A deleted key keeps its row with an empty value.
type Row struct {
	Value   []byte
	Version uint64
	Deleted bool
}

// Store abstracts state retrieval from the committed world state.
type Store interface {
	// GetRows returns the rows that exist for keys. Absent keys are omitted.
	GetRows(ctx context.Context, namespace string, keys []string) (map[string]Row, error)
	// ListPrefix returns all live rows whose key starts with prefix.
	ListPrefix(ctx context.Context, namespace, prefix string) (map[string]Row, error)
	// NamespaceVersion returns the namespace (policy) version a transaction must reference.
	NamespaceVersion(ctx context.Context, namespace string) (uint64, error)
	Close() error
}
