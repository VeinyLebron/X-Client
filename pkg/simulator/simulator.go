package simulator

import (
	"bytes"
	"context"
	"encoding/json"
	"slices"
	"sort"
	"sync"

	"x-client/pkg/models"
	"x-client/pkg/store"
)

type readOp struct {
	key string
	ver *uint64
}

type writeOp struct {
	key string
	val []byte
}

type nsSimulator struct {
	ns      string
	reads   []readOp
	writes  []writeOp
	deletes []string
}

// MultiNamespaceSimulator manages simulation state across multiple Fabric-X namespaces.
type MultiNamespaceSimulator struct {
	ctx        context.Context
	store      store.Store
	mu         sync.Mutex
	namespaces map[string]*nsSimulator
}

func NewMultiNamespaceSimulator(ctx context.Context, s store.Store) *MultiNamespaceSimulator {
	return &MultiNamespaceSimulator{
		ctx:        ctx,
		store:      s,
		namespaces: make(map[string]*nsSimulator),
	}
}

func (s *MultiNamespaceSimulator) getNS(ns string) *nsSimulator {
	if sim, ok := s.namespaces[ns]; ok {
		return sim
	}
	sim := &nsSimulator{
		ns: ns,
	}
	s.namespaces[ns] = sim
	return sim
}

// Get reads key in namespace. It checks buffered writes first, then queries the store.
func (s *MultiNamespaceSimulator) Get(namespace, key string) ([]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	sim := s.getNS(namespace)

	// Check if deleted in this tx
	if slices.Contains(sim.deletes, key) {
		return nil, nil
	}

	// Check buffered writes
	idx := slices.IndexFunc(sim.writes, func(w writeOp) bool { return w.key == key })
	if idx >= 0 {
		return sim.writes[idx].val, nil
	}

	// Query store
	val, ver, err := s.store.GetRow(s.ctx, namespace, key)
	if err != nil {
		return nil, err
	}

	// Record read (first read only)
	if !slices.ContainsFunc(sim.reads, func(r readOp) bool { return r.key == key }) {
		sim.reads = append(sim.reads, readOp{key: key, ver: ver})
	}

	return val, nil
}

// Put writes key in namespace.
func (s *MultiNamespaceSimulator) Put(namespace, key string, val []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	sim := s.getNS(namespace)

	// Remove from deletes if re-added
	if dIdx := slices.Index(sim.deletes, key); dIdx >= 0 {
		sim.deletes = append(sim.deletes[:dIdx], sim.deletes[dIdx+1:]...)
	}

	idx := slices.IndexFunc(sim.writes, func(w writeOp) bool { return w.key == key })
	if idx >= 0 {
		sim.writes[idx].val = bytes.Clone(val)
		return nil
	}
	sim.writes = append(sim.writes, writeOp{key: key, val: bytes.Clone(val)})
	return nil
}

// Del deletes key in namespace.
func (s *MultiNamespaceSimulator) Del(namespace, key string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	sim := s.getNS(namespace)

	// Remove from writes if present
	if idx := slices.IndexFunc(sim.writes, func(w writeOp) bool { return w.key == key }); idx >= 0 {
		sim.writes = append(sim.writes[:idx], sim.writes[idx+1:]...)
	}

	if !slices.Contains(sim.deletes, key) {
		sim.deletes = append(sim.deletes, key)
	}
	return nil
}

// PutJSON is a convenience helper to serialize and write JSON structs.
func (s *MultiNamespaceSimulator) PutJSON(namespace, key string, v any) error {
	data, err := json.Marshal(v)
	if err != nil {
		return err
	}
	return s.Put(namespace, key, data)
}

// GetJSON is a convenience helper to read and unmarshal JSON structs.
func (s *MultiNamespaceSimulator) GetJSON(namespace, key string, v any) (bool, error) {
	data, err := s.Get(namespace, key)
	if err != nil {
		return false, err
	}
	if len(data) == 0 {
		return false, nil
	}
	return true, json.Unmarshal(data, v)
}

// ListPrefix finds keys by prefix in the store, factoring in local buffered writes.
func (s *MultiNamespaceSimulator) ListPrefix(namespace, prefix string) (map[string][]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	sim := s.getNS(namespace)

	storeRows, err := s.store.ListPrefix(s.ctx, namespace, prefix)
	if err != nil {
		return nil, err
	}

	result := make(map[string][]byte)
	for k, row := range storeRows {
		// Record read in simulator
		if !slices.ContainsFunc(sim.reads, func(r readOp) bool { return r.key == k }) {
			sim.reads = append(sim.reads, readOp{key: k, ver: row.Version})
		}
		result[k] = row.Value
	}

	// Apply buffered writes
	for _, w := range sim.writes {
		if len(w.key) >= len(prefix) && w.key[:len(prefix)] == prefix {
			result[w.key] = w.val
		}
	}

	// Remove deletes
	for _, delKey := range sim.deletes {
		delete(result, delKey)
	}

	return result, nil
}

// BuildReadWriteSets constructs the deterministic Fabric-X Read/Write sets.
func (s *MultiNamespaceSimulator) BuildReadWriteSets() (map[string]models.NamespaceReadWriteSet, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	rwSets := make(map[string]models.NamespaceReadWriteSet)

	for ns, sim := range s.namespaces {
		nsVer, err := s.store.GetNamespaceVersion(s.ctx, ns)
		if err != nil {
			nsVer = 1
		}

		nsSet := models.NamespaceReadWriteSet{
			Namespace:   ns,
			NsVersion:   nsVer,
			ReadsOnly:   make([]models.ReadRecord, 0),
			ReadWrites:  make([]models.WriteRecord, 0),
			BlindWrites: make([]models.WriteRecord, 0),
		}

		// Process reads
		for _, r := range sim.reads {
			// Check if also written
			wIdx := slices.IndexFunc(sim.writes, func(w writeOp) bool { return w.key == r.key })
			dIdx := slices.Index(sim.deletes, r.key)

			if wIdx >= 0 {
				// Read-Write
				nsSet.ReadWrites = append(nsSet.ReadWrites, models.WriteRecord{
					Key:     r.key,
					Version: r.ver,
					Value:   string(sim.writes[wIdx].val),
				})
			} else if dIdx >= 0 {
				// Read and deleted: tombstone
				nsSet.ReadWrites = append(nsSet.ReadWrites, models.WriteRecord{
					Key:     r.key,
					Version: r.ver,
					Value:   "",
				})
			} else {
				// Read only
				nsSet.ReadsOnly = append(nsSet.ReadsOnly, models.ReadRecord{
					Key:     r.key,
					Version: r.ver,
				})
			}
		}

		// Process writes that were not read (blind writes)
		for _, w := range sim.writes {
			rIdx := slices.IndexFunc(sim.reads, func(r readOp) bool { return r.key == w.key })
			if rIdx < 0 {
				nsSet.BlindWrites = append(nsSet.BlindWrites, models.WriteRecord{
					Key:   w.key,
					Value: string(w.val),
				})
			}
		}

		// Process blind deletes (tombstones)
		for _, dKey := range sim.deletes {
			rIdx := slices.IndexFunc(sim.reads, func(r readOp) bool { return r.key == dKey })
			if rIdx < 0 {
				nsSet.BlindWrites = append(nsSet.BlindWrites, models.WriteRecord{
					Key:   dKey,
					Value: "",
				})
			}
		}

		// Sort keys deterministically
		sort.Slice(nsSet.ReadsOnly, func(i, j int) bool { return nsSet.ReadsOnly[i].Key < nsSet.ReadsOnly[j].Key })
		sort.Slice(nsSet.ReadWrites, func(i, j int) bool { return nsSet.ReadWrites[i].Key < nsSet.ReadWrites[j].Key })
		sort.Slice(nsSet.BlindWrites, func(i, j int) bool { return nsSet.BlindWrites[i].Key < nsSet.BlindWrites[j].Key })

		// Only include namespace if it participated in reads, writes, or deletes
		if len(nsSet.ReadsOnly) > 0 || len(nsSet.ReadWrites) > 0 || len(nsSet.BlindWrites) > 0 {
			rwSets[ns] = nsSet
		}
	}

	return rwSets, nil
}
