// Package engine drives a CBDC transaction through the Fabric-X lifecycle: execute the
// contract, collect the endorsements every namespace policy requires (locally or from
// peer organizations), wrap the transaction in a signed envelope and submit it.
package engine

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"slices"
	"sort"
	"sync"
	"time"

	"github.com/hyperledger/fabric-x-common/api/applicationpb"
	"github.com/hyperledger/fabric-x-common/api/committerpb"
	"google.golang.org/protobuf/proto"

	"x-client/pkg/contract"
	"x-client/pkg/endorser"
	"x-client/pkg/fabricx"
	"x-client/pkg/models"
	"x-client/pkg/peers"
	"x-client/pkg/simulator"
	"x-client/pkg/store"
)

// MaxClockSkew bounds how far a peer request's timestamp may be from local time.
const MaxClockSkew = 5 * time.Minute

// peerRetryDelay is how long to wait before retrying a peer whose state view lagged.
const peerRetryDelay = 500 * time.Millisecond

// CodeProposalMismatch is the API error code for ErrProposalMismatch.
const CodeProposalMismatch = "PROPOSAL_MISMATCH"

var (
	// ErrProposalMismatch means a peer's re-execution produced a different read/write set.
	ErrProposalMismatch = errors.New("proposal does not match local execution")
	// ErrNoStateChange means the operation produced no writes, so there is nothing to commit.
	ErrNoStateChange = errors.New("operation does not change state")
)

// IdentityError means this process cannot act as or obtain an endorsement from an MSP.
type IdentityError struct{ Msg string }

func (e *IdentityError) Error() string { return e.Msg }

// TxError reports a transaction that was ordered but not committed.
type TxError struct {
	TxID   string
	Status committerpb.Status
}

func (e *TxError) Error() string {
	return fmt.Sprintf("transaction %s was not committed: %s", e.TxID, e.Status)
}

// Config wires an Engine.
type Config struct {
	Contract  *contract.CBDCService
	Store     store.Store
	Registry  *endorser.Registry // identities this process signs with
	Policies  Policies           // namespace -> required MSPs
	Peers     *peers.Client      // endorsers of other organizations; nil if all keys are local
	Submitter fabricx.Submitter
	Channel   string
	Now       func() time.Time
}

// Engine executes CBDC operations end to end.
type Engine struct {
	cfg Config
}

func New(cfg Config) *Engine {
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	return &Engine{cfg: cfg}
}

// Result describes what happened to a request.
type Result struct {
	TxID         string
	Status       string
	Payload      any
	Namespaces   []*applicationpb.TxNamespace
	Endorsements []models.EndorsementInfo
	Envelope     []byte // proto-encoded common.Envelope; set when not submitted
}

// Query runs a read-only contract call. Nothing is signed or submitted.
func (e *Engine) Query(ctx context.Context, fn func(*simulator.MultiNamespaceSimulator) (any, error)) (*Result, error) {
	sim := simulator.NewMultiNamespaceSimulator(ctx, e.cfg.Store)
	payload, err := fn(sim)
	if err != nil {
		return nil, err
	}
	nss, err := sim.BuildTxNamespaces()
	if err != nil {
		return nil, err
	}
	return &Result{Status: models.TxStatusQueried, Payload: payload, Namespaces: nss}, nil
}

// Invoke executes op as callerMSP, gathers all required endorsements and, when submit
// is set, orders the transaction and waits for its commit status.
func (e *Engine) Invoke(ctx context.Context, callerMSP, op string, req any, submit bool) (*Result, error) {
	creator, ok := e.cfg.Registry.Get(callerMSP)
	if !ok {
		return nil, &contract.Error{Code: contract.ErrAccessDenied, Msg: fmt.Sprintf("this X-Client holds no key for %s and cannot act as it", callerMSP)}
	}
	reqJSON, err := json.Marshal(req)
	if err != nil {
		return nil, err
	}
	hdr, err := fabricx.NewHeader(creator)
	if err != nil {
		return nil, err
	}
	txc := contract.TxContext{
		TxID:      hdr.TxID,
		Timestamp: e.cfg.Now().UTC().Truncate(time.Second).Format(time.RFC3339),
		CallerMSP: callerMSP,
	}

	payload, nss, err := e.execute(ctx, txc, op, reqJSON)
	if err != nil {
		return nil, err
	}

	endorsements, err := e.collectEndorsements(ctx, creator, txc, op, reqJSON, nss)
	if err != nil {
		return nil, err
	}
	tx := &applicationpb.Tx{Namespaces: nss, Endorsements: endorsements}
	env, err := fabricx.NewEnvelope(e.cfg.Channel, hdr, creator, tx)
	if err != nil {
		return nil, err
	}

	res := &Result{
		TxID:         hdr.TxID,
		Payload:      payload,
		Namespaces:   nss,
		Endorsements: describe(nss, endorsements),
	}
	if !submit {
		res.Status = models.TxStatusEndorsed
		res.Envelope, err = proto.Marshal(env)
		return res, err
	}

	status, err := e.cfg.Submitter.Submit(ctx, hdr.TxID, env, tx)
	switch {
	case errors.Is(err, fabricx.ErrStatusTimeout):
		res.Status = models.TxStatusSubmitted
		return res, nil
	case err != nil:
		return nil, fmt.Errorf("submit %s: %w", hdr.TxID, err)
	case status != committerpb.Status_COMMITTED:
		return nil, &TxError{TxID: hdr.TxID, Status: status}
	}
	res.Status = models.TxStatusCommitted
	log.Printf("[Tx] %s op=%s caller=%s committed", hdr.TxID, op, callerMSP)
	return res, nil
}

// execute runs op on a fresh simulator and returns the resulting namespaces.
func (e *Engine) execute(ctx context.Context, txc contract.TxContext, op string, reqJSON json.RawMessage) (any, []*applicationpb.TxNamespace, error) {
	sim := simulator.NewMultiNamespaceSimulator(ctx, e.cfg.Store)
	payload, err := e.cfg.Contract.Execute(sim, txc, op, reqJSON)
	if err != nil {
		return nil, nil, err
	}
	nss, err := sim.BuildTxNamespaces()
	if err != nil {
		return nil, nil, err
	}
	nss = withWrites(nss)
	if len(nss) == 0 {
		return nil, nil, ErrNoStateChange
	}
	return payload, nss, nil
}

// withWrites drops namespaces the transaction only reads: the committer rejects any
// namespace without writes (MALFORMED_NO_WRITES), so those reads cannot be MVCC-checked.
// The contract only reads immutable facts (wallet ownership) outside written namespaces.
func withWrites(nss []*applicationpb.TxNamespace) []*applicationpb.TxNamespace {
	out := nss[:0]
	for _, ns := range nss {
		if len(ns.ReadWrites) > 0 || len(ns.BlindWrites) > 0 {
			out = append(out, ns)
		}
	}
	return out
}

// collectEndorsements returns, for every namespace, one endorsement per MSP its policy
// requires, signing locally where possible and asking peer organizations otherwise.
func (e *Engine) collectEndorsements(ctx context.Context, creator *endorser.Identity, txc contract.TxContext, op string, reqJSON json.RawMessage, nss []*applicationpb.TxNamespace) ([]*applicationpb.Endorsements, error) {
	required := make([][]string, len(nss))
	var remote []string
	for i, ns := range nss {
		msps, err := e.cfg.Policies.Required(ns.NsId)
		if err != nil {
			return nil, err
		}
		required[i] = msps
		for _, msp := range msps {
			if _, local := e.cfg.Registry.Get(msp); !local && !slices.Contains(remote, msp) {
				remote = append(remote, msp)
			}
		}
	}

	byMSP := make(map[string]map[string]*applicationpb.EndorsementWithIdentity) // msp -> ns -> endorsement
	for i, ns := range nss {
		for _, msp := range required[i] {
			id, local := e.cfg.Registry.Get(msp)
			if !local {
				continue
			}
			end, err := id.EndorseNamespace(ns, txc.TxID, nil)
			if err != nil {
				return nil, err
			}
			if byMSP[msp] == nil {
				byMSP[msp] = make(map[string]*applicationpb.EndorsementWithIdentity)
			}
			byMSP[msp][ns.NsId] = end
		}
	}

	if len(remote) > 0 {
		got, err := e.requestPeers(ctx, creator, txc, op, reqJSON, nss, remote)
		if err != nil {
			return nil, err
		}
		for msp, ends := range got {
			byMSP[msp] = ends
		}
	}

	out := make([]*applicationpb.Endorsements, len(nss))
	for i, ns := range nss {
		set := &applicationpb.Endorsements{}
		for _, msp := range required[i] {
			end, ok := byMSP[msp][ns.NsId]
			if !ok {
				return nil, &IdentityError{Msg: fmt.Sprintf("namespace %s requires an endorsement from %s, which was not provided", ns.NsId, msp)}
			}
			set.EndorsementsWithIdentity = append(set.EndorsementsWithIdentity, end)
		}
		out[i] = set
	}
	return out, nil
}

// requestPeers asks each remote MSP, in parallel, to endorse the proposal and verifies
// the signatures it returns.
func (e *Engine) requestPeers(ctx context.Context, creator *endorser.Identity, txc contract.TxContext, op string, reqJSON json.RawMessage, nss []*applicationpb.TxNamespace, msps []string) (map[string]map[string]*applicationpb.EndorsementWithIdentity, error) {
	if e.cfg.Peers == nil {
		return nil, &IdentityError{Msg: fmt.Sprintf("endorsements from %v are required but no peers are configured", msps)}
	}
	encoded := make([][]byte, len(nss))
	byID := make(map[string]*applicationpb.TxNamespace, len(nss))
	for i, ns := range nss {
		raw, err := proto.Marshal(ns)
		if err != nil {
			return nil, err
		}
		encoded[i] = raw
		byID[ns.NsId] = ns
	}
	req := peers.EndorseRequest{Op: op, Request: reqJSON, TxID: txc.TxID, Timestamp: txc.Timestamp, Namespaces: encoded}

	var (
		mu   sync.Mutex
		wg   sync.WaitGroup
		out  = make(map[string]map[string]*applicationpb.EndorsementWithIdentity, len(msps))
		errs []error
	)
	for _, msp := range msps {
		if !e.cfg.Peers.Has(msp) {
			return nil, &IdentityError{Msg: fmt.Sprintf("endorsement from %s is required but no peer endpoint is configured for it", msp)}
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			ends, err := e.requestPeer(ctx, creator, msp, req, byID, txc.TxID)
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				errs = append(errs, err)
				return
			}
			out[msp] = ends
		}()
	}
	wg.Wait()
	if len(errs) > 0 {
		return nil, errors.Join(errs...)
	}
	return out, nil
}

func (e *Engine) requestPeer(ctx context.Context, creator *endorser.Identity, msp string, req peers.EndorseRequest, byID map[string]*applicationpb.TxNamespace, txID string) (map[string]*applicationpb.EndorsementWithIdentity, error) {
	resp, err := e.cfg.Peers.Endorse(ctx, msp, creator, req)
	var perr *peers.Error
	if errors.As(err, &perr) && perr.Code == CodeProposalMismatch {
		// The peer's Query Service may not have caught up with the latest block yet.
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(peerRetryDelay):
		}
		resp, err = e.cfg.Peers.Endorse(ctx, msp, creator, req)
	}
	if err != nil {
		return nil, err
	}

	out := make(map[string]*applicationpb.EndorsementWithIdentity, len(resp.Endorsements))
	for _, ne := range resp.Endorsements {
		ns, ok := byID[ne.Namespace]
		if !ok {
			return nil, fmt.Errorf("peer %s endorsed unknown namespace %s", msp, ne.Namespace)
		}
		end := &applicationpb.EndorsementWithIdentity{}
		if err := proto.Unmarshal(ne.Endorsement, end); err != nil {
			return nil, fmt.Errorf("peer %s: decode endorsement: %w", msp, err)
		}
		if got := end.GetIdentity().GetMspId(); got != msp {
			return nil, fmt.Errorf("peer %s returned an endorsement signed as %s", msp, got)
		}
		if err := endorser.VerifyEndorsement(ns, txID, nil, end); err != nil {
			return nil, fmt.Errorf("peer %s: %w", msp, err)
		}
		out[ne.Namespace] = end
	}
	return out, nil
}

// EndorseForPeer re-executes a peer organization's proposal on this organization's view
// of the ledger and, if the result is identical, endorses every namespace whose policy
// requires one of this process's identities.
func (e *Engine) EndorseForPeer(ctx context.Context, req *peers.EndorseRequest) (*peers.EndorseResponse, error) {
	ts, err := time.Parse(time.RFC3339, req.Timestamp)
	if err != nil {
		return nil, &contract.Error{Code: contract.ErrInvalidArgument, Msg: "invalid timestamp"}
	}
	if skew := e.cfg.Now().Sub(ts); skew > MaxClockSkew || skew < -MaxClockSkew {
		return nil, &contract.Error{Code: contract.ErrInvalidArgument, Msg: fmt.Sprintf("timestamp %s is outside the allowed clock skew", req.Timestamp)}
	}
	proposed, err := req.TxNamespaces()
	if err != nil {
		return nil, &contract.Error{Code: contract.ErrInvalidArgument, Msg: err.Error()}
	}

	txc := contract.TxContext{TxID: req.TxID, Timestamp: req.Timestamp, CallerMSP: req.InitiatorMSP}
	_, local, err := e.execute(ctx, txc, req.Op, req.Request)
	if err != nil {
		return nil, err
	}
	if err := sameNamespaces(local, proposed, req.TxID); err != nil {
		return nil, err
	}

	resp := &peers.EndorseResponse{}
	for _, ns := range local {
		msps, err := e.cfg.Policies.Required(ns.NsId)
		if err != nil {
			return nil, err
		}
		for _, msp := range msps {
			id, ok := e.cfg.Registry.Get(msp)
			if !ok {
				continue
			}
			end, err := id.EndorseNamespace(ns, req.TxID, nil)
			if err != nil {
				return nil, err
			}
			raw, err := proto.Marshal(end)
			if err != nil {
				return nil, err
			}
			resp.Endorsements = append(resp.Endorsements, peers.NamespaceEndorsement{Namespace: ns.NsId, Endorsement: raw})
		}
	}
	log.Printf("[Endorse] %s op=%s for %s: %d namespace endorsement(s)", req.TxID, req.Op, req.InitiatorMSP, len(resp.Endorsements))
	return resp, nil
}

// sameNamespaces compares the exact bytes each endorser would sign.
func sameNamespaces(local, proposed []*applicationpb.TxNamespace, txID string) error {
	if len(local) != len(proposed) {
		return fmt.Errorf("%w: %d namespaces locally, %d proposed", ErrProposalMismatch, len(local), len(proposed))
	}
	for i := range local {
		a, err := local[i].ASN1Marshal(txID, nil)
		if err != nil {
			return err
		}
		b, err := proposed[i].ASN1Marshal(txID, nil)
		if err != nil {
			return err
		}
		if !bytes.Equal(a, b) {
			return fmt.Errorf("%w: namespace %s differs", ErrProposalMismatch, local[i].NsId)
		}
	}
	return nil
}

func describe(nss []*applicationpb.TxNamespace, ends []*applicationpb.Endorsements) []models.EndorsementInfo {
	var out []models.EndorsementInfo
	for i, ns := range nss {
		for _, e := range ends[i].EndorsementsWithIdentity {
			out = append(out, models.EndorsementInfo{
				Namespace:   ns.NsId,
				EndorserMSP: e.GetIdentity().GetMspId(),
				Signature:   fmt.Sprintf("%x", e.Endorsement),
			})
		}
	}
	return out
}

// ─── Policies ────────────────────────────────────────────────────────────────

// Policies maps each namespace to the MSPs that must all endorse it. It must mirror the
// policies the namespaces were registered with (see devnet-3org `make init-namespace-cbdc`).
type Policies map[string][]string

// DefaultPolicies returns the wholesale CBDC policy layout: the issuer (central bank)
// co-signs every namespace and each bank additionally signs its own balance namespace.
// A bank that is also the issuer signs its balance namespace alone.
func DefaultPolicies(svc *contract.CBDCService, bankMSPs []string) Policies {
	ns := svc.Namespaces()
	issuer := ns.CentralBankMSP
	p := Policies{
		ns.Public:  {issuer},
		ns.Supply:  {issuer},
		ns.Credits: {issuer},
	}
	for _, msp := range bankMSPs {
		signers := []string{msp}
		if msp != issuer {
			signers = append(signers, issuer)
		}
		p[svc.BalanceNamespace(msp)] = signers
	}
	return p
}

// Required returns the MSPs that must endorse namespace.
func (p Policies) Required(namespace string) ([]string, error) {
	msps, ok := p[namespace]
	if !ok || len(msps) == 0 {
		return nil, &IdentityError{Msg: fmt.Sprintf("no endorsement policy configured for namespace %s", namespace)}
	}
	return msps, nil
}

// MSPs returns every MSP named by any policy, sorted.
func (p Policies) MSPs() []string {
	var out []string
	for _, msps := range p {
		for _, m := range msps {
			if !slices.Contains(out, m) {
				out = append(out, m)
			}
		}
	}
	sort.Strings(out)
	return out
}
