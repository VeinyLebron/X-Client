package store

import (
	"bytes"
	"context"
	"fmt"
	"net"
	"sync"
	"time"

	"github.com/hyperledger/fabric-x-common/api/committerpb"
	"github.com/hyperledger/fabric-x-sdk/network"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/protobuf/types/known/emptypb"
)

// policyCacheTTL bounds how long namespace versions are cached between lookups.
const policyCacheTTL = 30 * time.Second

// GRPCConfig configures the connection to a Fabric-X Query Service.
type GRPCConfig struct {
	Endpoint string // e.g. "localhost:7001" or "committer-query-service:7001"
	TLS      network.TLSConfig
}

// GRPCQueryServiceStore reads committed state from the Fabric-X Query Service.
type GRPCQueryServiceStore struct {
	conn   *grpc.ClientConn
	client committerpb.QueryServiceClient
	index  *KeyIndex

	mu         sync.Mutex
	nsVersions map[string]uint64
	fetchedAt  time.Time
}

// DialTLS returns gRPC transport credentials for cfg, validating the server certificate.
func DialTLS(endpoint string, cfg network.TLSConfig) (grpc.DialOption, error) {
	if cfg.Mode == "" || cfg.Mode == network.TLSModeNone {
		return grpc.WithTransportCredentials(insecure.NewCredentials()), nil
	}
	host, _, err := net.SplitHostPort(endpoint)
	if err != nil {
		return nil, fmt.Errorf("%s: address must contain port: %w", endpoint, err)
	}
	tlsCfg, err := cfg.LoadClientTLSConfig(host)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", endpoint, err)
	}
	return grpc.WithTransportCredentials(credentials.NewTLS(tlsCfg)), nil
}

// NewGRPCQueryServiceStore connects to the Query Service. index resolves prefix scans
// and must be fed by a block synchronizer.
func NewGRPCQueryServiceStore(cfg GRPCConfig, index *KeyIndex) (*GRPCQueryServiceStore, error) {
	creds, err := DialTLS(cfg.Endpoint, cfg.TLS)
	if err != nil {
		return nil, fmt.Errorf("query service TLS: %w", err)
	}
	conn, err := grpc.NewClient(cfg.Endpoint, creds)
	if err != nil {
		return nil, fmt.Errorf("dial query service at %s: %w", cfg.Endpoint, err)
	}
	return &GRPCQueryServiceStore{
		conn:   conn,
		client: committerpb.NewQueryServiceClient(conn),
		index:  index,
	}, nil
}

func (s *GRPCQueryServiceStore) Close() error {
	return s.conn.Close()
}

// Client exposes the Query Service client, e.g. for transaction status lookups.
func (s *GRPCQueryServiceStore) Client() committerpb.QueryServiceClient {
	return s.client
}

func (s *GRPCQueryServiceStore) GetRows(ctx context.Context, namespace string, keys []string) (map[string]Row, error) {
	out := make(map[string]Row, len(keys))
	if len(keys) == 0 {
		return out, nil
	}
	byteKeys := make([][]byte, len(keys))
	for i, k := range keys {
		byteKeys[i] = []byte(k)
	}

	resp, err := s.client.GetRows(ctx, &committerpb.Query{
		Namespaces: []*committerpb.QueryNamespace{{NsId: namespace, Keys: byteKeys}},
	})
	if err != nil {
		return nil, fmt.Errorf("query service GetRows namespace=%s: %w", namespace, err)
	}

	for _, nsRows := range resp.GetNamespaces() {
		for _, row := range nsRows.GetRows() {
			out[string(row.Key)] = Row{
				Value:   bytes.Clone(row.Value),
				Version: row.Version,
				Deleted: len(row.Value) == 0,
			}
		}
	}
	return out, nil
}

func (s *GRPCQueryServiceStore) ListPrefix(ctx context.Context, namespace, prefix string) (map[string]Row, error) {
	rows, err := s.GetRows(ctx, namespace, s.index.Keys(namespace, prefix))
	if err != nil {
		return nil, err
	}
	for k, row := range rows {
		if row.Deleted {
			delete(rows, k)
		}
	}
	return rows, nil
}

func (s *GRPCQueryServiceStore) NamespaceVersion(ctx context.Context, namespace string) (uint64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if v, ok := s.nsVersions[namespace]; ok && time.Since(s.fetchedAt) < policyCacheTTL {
		return v, nil
	}

	pols, err := s.client.GetNamespacePolicies(ctx, &emptypb.Empty{})
	if err != nil {
		return 0, fmt.Errorf("query service GetNamespacePolicies: %w", err)
	}
	s.nsVersions = make(map[string]uint64, len(pols.GetPolicies()))
	for _, p := range pols.GetPolicies() {
		s.nsVersions[p.Namespace] = p.Version
	}
	s.fetchedAt = time.Now()

	v, ok := s.nsVersions[namespace]
	if !ok {
		return 0, fmt.Errorf("namespace %s is not registered on the network", namespace)
	}
	return v, nil
}
