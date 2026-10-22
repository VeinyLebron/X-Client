package fabricx

import (
	"context"
	"errors"
	"fmt"
	"log"
	"time"

	"github.com/hyperledger/fabric-protos-go-apiv2/common"
	"github.com/hyperledger/fabric-x-common/api/applicationpb"
	"github.com/hyperledger/fabric-x-common/api/committerpb"
	"github.com/hyperledger/fabric-x-sdk/network"

	"x-client/pkg/store"
)

// ErrStatusTimeout means the transaction was submitted but no final status was observed in time.
var ErrStatusTimeout = errors.New("timed out waiting for transaction status")

// Submitter orders a signed transaction and reports its final ledger status.
type Submitter interface {
	Submit(ctx context.Context, txID string, env *common.Envelope, tx *applicationpb.Tx) (committerpb.Status, error)
	Close() error
}

// ─── Network submitter ───────────────────────────────────────────────────────

// NetworkSubmitter broadcasts envelopes to the Arma routers and polls the Query Service
// for the commit status.
type NetworkSubmitter struct {
	orderers     []*network.Orderer
	addrs        []string
	status       committerpb.QueryServiceClient
	timeout      time.Duration
	pollInterval time.Duration
}

// NewNetworkSubmitter dials every router. ctx bounds the lifetime of the broadcast streams.
func NewNetworkSubmitter(ctx context.Context, orderers []network.OrdererConf, status committerpb.QueryServiceClient, timeout time.Duration) (*NetworkSubmitter, error) {
	if len(orderers) == 0 {
		return nil, errors.New("no orderers configured")
	}
	s := &NetworkSubmitter{status: status, timeout: timeout, pollInterval: 250 * time.Millisecond}
	for _, conf := range orderers {
		o, err := network.NewOrderer(ctx, conf)
		if err != nil {
			s.Close()
			return nil, err
		}
		s.orderers = append(s.orderers, o)
		s.addrs = append(s.addrs, conf.Address)
	}
	return s, nil
}

// Submit sends env to every router (Arma de-duplicates) and waits for the committed status.
// It fails if a majority of routers could not be reached.
func (s *NetworkSubmitter) Submit(ctx context.Context, txID string, env *common.Envelope, _ *applicationpb.Tx) (committerpb.Status, error) {
	var failed int
	for i, o := range s.orderers {
		if err := o.Broadcast(ctx, env); err != nil {
			log.Printf("[Submit] tx=%s router %s: %v", txID, s.addrs[i], err)
			failed++
		}
	}
	if failed*2 > len(s.orderers) {
		return committerpb.Status_STATUS_UNSPECIFIED, fmt.Errorf("broadcast failed on %d of %d routers", failed, len(s.orderers))
	}
	return s.waitForStatus(ctx, txID)
}

func (s *NetworkSubmitter) waitForStatus(ctx context.Context, txID string) (committerpb.Status, error) {
	ctx, cancel := context.WithTimeout(ctx, s.timeout)
	defer cancel()

	ticker := time.NewTicker(s.pollInterval)
	defer ticker.Stop()
	for {
		resp, err := s.status.GetTransactionStatus(ctx, &committerpb.TxStatusQuery{TxIds: []string{txID}})
		if err == nil {
			for _, st := range resp.GetStatuses() {
				if st.GetRef().GetTxId() == txID && st.GetStatus() != committerpb.Status_STATUS_UNSPECIFIED {
					return st.GetStatus(), nil
				}
			}
		} else if ctx.Err() == nil {
			log.Printf("[Submit] tx=%s status query: %v", txID, err)
		}

		select {
		case <-ctx.Done():
			return committerpb.Status_STATUS_UNSPECIFIED, ErrStatusTimeout
		case <-ticker.C:
		}
	}
}

func (s *NetworkSubmitter) Close() error {
	var errs []error
	for _, o := range s.orderers {
		errs = append(errs, o.Close())
	}
	return errors.Join(errs...)
}

// ─── Local submitter ─────────────────────────────────────────────────────────

// LocalSubmitter commits directly to an in-memory ledger (standalone mode).
type LocalSubmitter struct {
	Store *store.InMemoryStore
}

func (s LocalSubmitter) Submit(_ context.Context, txID string, _ *common.Envelope, tx *applicationpb.Tx) (committerpb.Status, error) {
	return s.Store.Commit(txID, tx), nil
}

func (LocalSubmitter) Close() error { return nil }
