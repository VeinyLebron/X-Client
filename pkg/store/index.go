package store

import (
	"context"
	"sort"
	"strings"
	"sync"

	"github.com/hyperledger/fabric-x-sdk/blocks"
)

// KeyIndex tracks which keys exist in each namespace by following committed blocks.
// The Query Service only serves point lookups, so prefix scans (pending credits,
// supply deltas) resolve key names here and fetch values from the Query Service.
//
// It implements blocks.BlockHandler and network.BlockHeightReader so it can be fed
// by a fabric-x-sdk Synchronizer. The index is rebuilt from block 0 on every start.
type KeyIndex struct {
	mu        sync.RWMutex
	keys      map[string]map[string]struct{}
	lastBlock uint64
}

func NewKeyIndex() *KeyIndex {
	return &KeyIndex{keys: make(map[string]map[string]struct{})}
}

// Handle records the writes of every valid transaction in b.
func (ix *KeyIndex) Handle(_ context.Context, b blocks.Block) error {
	ix.mu.Lock()
	defer ix.mu.Unlock()

	for _, tx := range b.Transactions {
		if !tx.Valid {
			continue
		}
		for _, ns := range tx.NsRWS {
			set, ok := ix.keys[ns.Namespace]
			if !ok {
				set = make(map[string]struct{})
				ix.keys[ns.Namespace] = set
			}
			for _, w := range ns.RWS.Writes {
				if w.IsDelete || len(w.Value) == 0 {
					delete(set, w.Key)
				} else {
					set[w.Key] = struct{}{}
				}
			}
		}
	}
	ix.lastBlock = b.Number
	return nil
}

// BlockNumber returns the last processed block number.
func (ix *KeyIndex) BlockNumber(context.Context) (uint64, error) {
	ix.mu.RLock()
	defer ix.mu.RUnlock()
	return ix.lastBlock, nil
}

// Keys returns the sorted live keys in namespace that start with prefix.
func (ix *KeyIndex) Keys(namespace, prefix string) []string {
	ix.mu.RLock()
	defer ix.mu.RUnlock()

	var out []string
	for k := range ix.keys[namespace] {
		if strings.HasPrefix(k, prefix) {
			out = append(out, k)
		}
	}
	sort.Strings(out)
	return out
}
