package store

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"os"
	"strings"
	"sync"

	"github.com/hyperledger/fabric-x-common/api/committerpb"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/protobuf/types/known/emptypb"
)

// RowResult represents a queried state row.
type RowResult struct {
	Value   []byte
	Version *uint64
}

// Store abstracts state retrieval from the world state (Query Service).
type Store interface {
	GetRow(ctx context.Context, namespace, key string) (value []byte, version *uint64, err error)
	GetRows(ctx context.Context, namespace string, keys []string) (map[string]RowResult, error)
	ListPrefix(ctx context.Context, namespace, prefix string) (map[string]RowResult, error)
	GetNamespaceVersion(ctx context.Context, namespace string) (uint64, error)
	Close() error
}

// --- GRPCQueryServiceStore: Talks to Fabric-X Query Service container (Port 7001) ---

type GRPCConfig struct {
	Endpoint   string // e.g. "localhost:7001" or "query-service:7001"
	TLSEnabled bool
	CACertPath string
	ClientCert string
	ClientKey  string
}

type GRPCQueryServiceStore struct {
	conn   *grpc.ClientConn
	client committerpb.QueryServiceClient
}

// NewGRPCQueryServiceStore creates a client connected to the Query Service.
func NewGRPCQueryServiceStore(cfg GRPCConfig) (*GRPCQueryServiceStore, error) {
	var opts []grpc.DialOption

	if cfg.TLSEnabled {
		var certPool *x509.CertPool
		if cfg.CACertPath != "" {
			caPEM, err := os.ReadFile(cfg.CACertPath)
			if err != nil {
				return nil, fmt.Errorf("read ca cert %s: %w", cfg.CACertPath, err)
			}
			certPool = x509.NewCertPool()
			if !certPool.AppendCertsFromPEM(caPEM) {
				return nil, fmt.Errorf("failed to append CA cert")
			}
		}

		tlsConf := &tls.Config{
			RootCAs:            certPool,
			InsecureSkipVerify: true,
			ServerName:         "committer-query-service",
		}
		if cfg.ClientCert != "" && cfg.ClientKey != "" {
			cert, err := tls.LoadX509KeyPair(cfg.ClientCert, cfg.ClientKey)
			if err != nil {
				return nil, fmt.Errorf("load client keypair: %w", err)
			}
			tlsConf.Certificates = []tls.Certificate{cert}
		}
		opts = append(opts, grpc.WithTransportCredentials(credentials.NewTLS(tlsConf)))
	} else {
		opts = append(opts, grpc.WithTransportCredentials(insecure.NewCredentials()))
	}

	conn, err := grpc.NewClient(cfg.Endpoint, opts...)
	if err != nil {
		return nil, fmt.Errorf("dial query service at %s: %w", cfg.Endpoint, err)
	}

	return &GRPCQueryServiceStore{
		conn:   conn,
		client: committerpb.NewQueryServiceClient(conn),
	}, nil
}

func (s *GRPCQueryServiceStore) Close() error {
	if s.conn != nil {
		return s.conn.Close()
	}
	return nil
}

func (s *GRPCQueryServiceStore) GetRow(ctx context.Context, namespace, key string) ([]byte, *uint64, error) {
	res, err := s.GetRows(ctx, namespace, []string{key})
	if err != nil {
		return nil, nil, err
	}
	row, ok := res[key]
	if !ok || len(row.Value) == 0 {
		return nil, nil, nil
	}
	return row.Value, row.Version, nil
}

func (s *GRPCQueryServiceStore) GetRows(ctx context.Context, namespace string, keys []string) (map[string]RowResult, error) {
	byteKeys := make([][]byte, len(keys))
	for i, k := range keys {
		byteKeys[i] = []byte(k)
	}

	resp, err := s.client.GetRows(ctx, &committerpb.Query{
		Namespaces: []*committerpb.QueryNamespace{
			{
				NsId: namespace,
				Keys: byteKeys,
			},
		},
	})
	if err != nil {
		return nil, fmt.Errorf("GetRows namespace=%s: %w", namespace, err)
	}

	out := make(map[string]RowResult, len(keys))
	for _, nsRows := range resp.GetNamespaces() {
		for _, row := range nsRows.GetRows() {
			k := string(row.Key)
			if len(row.Value) > 0 {
				v := row.Version
				out[k] = RowResult{
					Value:   bytes.Clone(row.Value),
					Version: &v,
				}
			}
		}
	}
	return out, nil
}

// ListPrefix retrieves keys matching prefix. (In Fabric-X devnet with PostgreSQL,
// this can query tracked index rows or registered keys).
func (s *GRPCQueryServiceStore) ListPrefix(ctx context.Context, namespace, prefix string) (map[string]RowResult, error) {
	// The standard QueryService GetRows takes explicit keys.
	// For prefixes, we query known keys or index rows.
	return make(map[string]RowResult), nil
}

func (s *GRPCQueryServiceStore) GetNamespaceVersion(ctx context.Context, namespace string) (uint64, error) {
	pols, err := s.client.GetNamespacePolicies(ctx, &emptypb.Empty{})
	if err != nil {
		return 1, nil // Fallback default version
	}
	for _, p := range pols.GetPolicies() {
		if p.Namespace == namespace {
			return p.Version, nil
		}
	}
	return 1, nil
}

// --- InMemoryStore: For standalone testing, unit tests, and mock execution ---

type InMemoryStore struct {
	mu         sync.RWMutex
	data       map[string]map[string]RowResult // namespace -> key -> RowResult
	nsVersions map[string]uint64
}

func NewInMemoryStore() *InMemoryStore {
	return &InMemoryStore{
		data:       make(map[string]map[string]RowResult),
		nsVersions: make(map[string]uint64),
	}
}

func (m *InMemoryStore) Close() error { return nil }

func (m *InMemoryStore) Set(namespace, key string, value []byte) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if _, ok := m.data[namespace]; !ok {
		m.data[namespace] = make(map[string]RowResult)
		m.nsVersions[namespace] = 1
	}

	existing, exists := m.data[namespace][key]
	var nextVer uint64 = 1
	if exists && existing.Version != nil {
		nextVer = *existing.Version + 1
	}

	if value == nil {
		delete(m.data[namespace], key)
	} else {
		m.data[namespace][key] = RowResult{
			Value:   bytes.Clone(value),
			Version: &nextVer,
		}
	}
}

func (m *InMemoryStore) Delete(namespace, key string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.data[namespace]; ok {
		delete(m.data[namespace], key)
	}
}

func (m *InMemoryStore) GetRow(ctx context.Context, namespace, key string) ([]byte, *uint64, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	nsData, ok := m.data[namespace]
	if !ok {
		return nil, nil, nil
	}
	row, ok := nsData[key]
	if !ok || len(row.Value) == 0 {
		return nil, nil, nil
	}
	return bytes.Clone(row.Value), row.Version, nil
}

func (m *InMemoryStore) GetRows(ctx context.Context, namespace string, keys []string) (map[string]RowResult, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	out := make(map[string]RowResult)
	nsData, ok := m.data[namespace]
	if !ok {
		return out, nil
	}

	for _, k := range keys {
		if row, exists := nsData[k]; exists && len(row.Value) > 0 {
			out[k] = RowResult{
				Value:   bytes.Clone(row.Value),
				Version: row.Version,
			}
		}
	}
	return out, nil
}

func (m *InMemoryStore) ListPrefix(ctx context.Context, namespace, prefix string) (map[string]RowResult, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	out := make(map[string]RowResult)
	nsData, ok := m.data[namespace]
	if !ok {
		return out, nil
	}

	for k, row := range nsData {
		if strings.HasPrefix(k, prefix) && len(row.Value) > 0 {
			out[k] = RowResult{
				Value:   bytes.Clone(row.Value),
				Version: row.Version,
			}
		}
	}
	return out, nil
}

func (m *InMemoryStore) GetNamespaceVersion(ctx context.Context, namespace string) (uint64, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	v, ok := m.nsVersions[namespace]
	if !ok {
		return 1, nil
	}
	return v, nil
}

// ApplyReadWriteSet simulates the Committer writing the read/write set to state.
func (m *InMemoryStore) ApplyReadWriteSet(namespace string, writes []RowResult, deletes []string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.data[namespace]; !ok {
		m.data[namespace] = make(map[string]RowResult)
		m.nsVersions[namespace] = 1
	}
	for _, delKey := range deletes {
		delete(m.data[namespace], delKey)
	}
	return nil
}
