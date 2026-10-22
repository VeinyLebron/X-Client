// Package simulator executes contract logic against committed state and records the
// reads and writes as Fabric-X TxNamespaces. A simulator is used by a single request.
package simulator

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/hyperledger/fabric-x-common/api/applicationpb"

	"x-client/pkg/models"
	"x-client/pkg/store"
)

type write struct {
	val []byte // nil deletes the key
}

type nsState struct {
	reads  map[string]*uint64 // key -> version read (nil: key absent)
	writes map[string]write
}

// MultiNamespaceSimulator records reads and writes across Fabric-X namespaces.
type MultiNamespaceSimulator struct {
	ctx        context.Context
	store      store.Store
	namespaces map[string]*nsState
}

func NewMultiNamespaceSimulator(ctx context.Context, s store.Store) *MultiNamespaceSimulator {
	return &MultiNamespaceSimulator{
		ctx:        ctx,
		store:      s,
		namespaces: make(map[string]*nsState),
	}
}

func (s *MultiNamespaceSimulator) ns(name string) *nsState {
	st, ok := s.namespaces[name]
	if !ok {
		st = &nsState{reads: make(map[string]*uint64), writes: make(map[string]write)}
		s.namespaces[name] = st
	}
	return st
}

// recordRead keeps the first version observed for key.
func (st *nsState) recordRead(key string, ver *uint64) {
	if _, ok := st.reads[key]; !ok {
		st.reads[key] = ver
	}
}

// Get reads key, returning this transaction's own pending write if there is one.
// Like the fabric-x-sdk simulation store, a deleted key is not recorded as a read.
func (s *MultiNamespaceSimulator) Get(namespace, key string) ([]byte, error) {
	st := s.ns(namespace)
	if w, ok := st.writes[key]; ok {
		return bytes.Clone(w.val), nil
	}

	rows, err := s.store.GetRows(s.ctx, namespace, []string{key})
	if err != nil {
		return nil, err
	}
	row, ok := rows[key]
	switch {
	case !ok:
		st.recordRead(key, nil)
		return nil, nil
	case row.Deleted:
		return nil, nil
	default:
		v := row.Version
		st.recordRead(key, &v)
		return row.Value, nil
	}
}

// Put writes key. An empty value deletes it.
func (s *MultiNamespaceSimulator) Put(namespace, key string, val []byte) error {
	if key == "" {
		return fmt.Errorf("empty key in namespace %s", namespace)
	}
	if len(val) == 0 {
		return s.Del(namespace, key)
	}
	s.ns(namespace).writes[key] = write{val: bytes.Clone(val)}
	return nil
}

// Del deletes key.
func (s *MultiNamespaceSimulator) Del(namespace, key string) error {
	if key == "" {
		return fmt.Errorf("empty key in namespace %s", namespace)
	}
	s.ns(namespace).writes[key] = write{}
	return nil
}

// PutJSON serializes v and writes it.
func (s *MultiNamespaceSimulator) PutJSON(namespace, key string, v any) error {
	data, err := json.Marshal(v)
	if err != nil {
		return err
	}
	return s.Put(namespace, key, data)
}

// GetJSON reads key into v, reporting whether the key exists.
func (s *MultiNamespaceSimulator) GetJSON(namespace, key string, v any) (bool, error) {
	data, err := s.Get(namespace, key)
	if err != nil || len(data) == 0 {
		return false, err
	}
	return true, json.Unmarshal(data, v)
}

// ListPrefix returns live keys starting with prefix, including this transaction's writes.
// Every committed key returned is recorded as a read.
func (s *MultiNamespaceSimulator) ListPrefix(namespace, prefix string) (map[string][]byte, error) {
	st := s.ns(namespace)
	rows, err := s.store.ListPrefix(s.ctx, namespace, prefix)
	if err != nil {
		return nil, err
	}

	result := make(map[string][]byte, len(rows))
	for k, row := range rows {
		v := row.Version
		st.recordRead(k, &v)
		result[k] = row.Value
	}
	for k, w := range st.writes {
		if !strings.HasPrefix(k, prefix) {
			continue
		}
		if w.val == nil {
			delete(result, k)
		} else {
			result[k] = w.val
		}
	}
	return result, nil
}

// BuildTxNamespaces returns the recorded read/write sets as Fabric-X TxNamespaces,
// sorted by namespace and key so every endorser derives identical bytes.
func (s *MultiNamespaceSimulator) BuildTxNamespaces() ([]*applicationpb.TxNamespace, error) {
	names := make([]string, 0, len(s.namespaces))
	for name, st := range s.namespaces {
		if len(st.reads) > 0 || len(st.writes) > 0 {
			names = append(names, name)
		}
	}
	sort.Strings(names)

	out := make([]*applicationpb.TxNamespace, 0, len(names))
	for _, name := range names {
		st := s.namespaces[name]
		nsVer, err := s.store.NamespaceVersion(s.ctx, name)
		if err != nil {
			return nil, err
		}
		txNs := &applicationpb.TxNamespace{NsId: name, NsVersion: nsVer}

		for _, key := range sortedKeys(st.reads) {
			ver := st.reads[key]
			if w, written := st.writes[key]; written {
				txNs.ReadWrites = append(txNs.ReadWrites, &applicationpb.ReadWrite{
					Key: []byte(key), Version: ver, Value: w.val,
				})
			} else {
				txNs.ReadsOnly = append(txNs.ReadsOnly, &applicationpb.Read{Key: []byte(key), Version: ver})
			}
		}
		for _, key := range sortedKeys(st.writes) {
			if _, read := st.reads[key]; !read {
				txNs.BlindWrites = append(txNs.BlindWrites, &applicationpb.Write{
					Key: []byte(key), Value: st.writes[key].val,
				})
			}
		}
		out = append(out, txNs)
	}
	return out, nil
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// View renders TxNamespaces as JSON-friendly read/write sets.
func View(nss []*applicationpb.TxNamespace) map[string]models.NamespaceReadWriteSet {
	out := make(map[string]models.NamespaceReadWriteSet, len(nss))
	for _, ns := range nss {
		v := models.NamespaceReadWriteSet{Namespace: ns.NsId, NsVersion: ns.NsVersion}
		for _, r := range ns.ReadsOnly {
			v.ReadsOnly = append(v.ReadsOnly, models.ReadRecord{Key: string(r.Key), Version: r.Version})
		}
		for _, rw := range ns.ReadWrites {
			v.ReadWrites = append(v.ReadWrites, models.WriteRecord{Key: string(rw.Key), Version: rw.Version, Value: string(rw.Value)})
		}
		for _, w := range ns.BlindWrites {
			v.BlindWrites = append(v.BlindWrites, models.WriteRecord{Key: string(w.Key), Value: string(w.Value)})
		}
		out[ns.NsId] = v
	}
	return out
}
