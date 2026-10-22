package engine_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"
	"time"

	"google.golang.org/protobuf/proto"

	"x-client/pkg/api"
	"x-client/pkg/contract"
	"x-client/pkg/endorser"
	"x-client/pkg/engine"
	"x-client/pkg/fabricx"
	"x-client/pkg/models"
	"x-client/pkg/peers"
	"x-client/pkg/simulator"
	"x-client/pkg/store"
)

const (
	cb    = models.CentralBankMSP
	bankA = "Org1MSP"
	bankB = "Org2MSP"
	salt  = "0123456789abcdef-salt"
)

var banks = []string{bankA, bankB}

func newService() *contract.CBDCService {
	return contract.NewCBDCService(contract.DefaultNamespaces())
}

// newUnified returns an engine holding every organization's key over one in-memory ledger.
func newUnified(t *testing.T) (*engine.Engine, *contract.CBDCService, *store.InMemoryStore) {
	t.Helper()
	svc := newService()
	mem := store.NewInMemoryStore()
	reg := endorser.NewRegistry(cb)
	for _, msp := range []string{cb, bankA, bankB} {
		id, err := endorser.Generate(msp)
		if err != nil {
			t.Fatal(err)
		}
		reg.Register(id)
	}
	eng := engine.New(engine.Config{
		Contract:  svc,
		Store:     mem,
		Registry:  reg,
		Policies:  engine.DefaultPolicies(svc, banks),
		Submitter: fabricx.LocalSubmitter{Store: mem},
		Channel:   "mychannel",
	})
	return eng, svc, mem
}

func invoke(t *testing.T, eng *engine.Engine, caller, op string, req any) *engine.Result {
	t.Helper()
	res, err := eng.Invoke(context.Background(), caller, op, req, true)
	if err != nil {
		t.Fatalf("%s as %s: %v", op, caller, err)
	}
	if res.Status != models.TxStatusCommitted {
		t.Fatalf("%s as %s: status %s", op, caller, res.Status)
	}
	return res
}

func expectCode(t *testing.T, err error, code string) {
	t.Helper()
	ce, ok := contract.AsError(err)
	if !ok || ce.Code != code {
		t.Fatalf("expected %s, got %v", code, err)
	}
}

func balance(t *testing.T, svc *contract.CBDCService, st store.Store, owner, wallet string) *models.BalanceView {
	t.Helper()
	sim := simulator.NewMultiNamespaceSimulator(context.Background(), st)
	bv, err := svc.CheckBalance(sim, owner, wallet)
	if err != nil {
		t.Fatalf("balance %s: %v", wallet, err)
	}
	return bv
}

func supply(t *testing.T, svc *contract.CBDCService, st store.Store) int64 {
	t.Helper()
	sim := simulator.NewMultiNamespaceSimulator(context.Background(), st)
	sv, err := svc.QueryTotalSupply(sim, cb)
	if err != nil {
		t.Fatal(err)
	}
	return sv.TotalSupply
}

func setupWallets(t *testing.T, eng *engine.Engine) {
	t.Helper()
	invoke(t, eng, cb, contract.OpCreateWallet, models.CreateWalletRequest{WalletID: "WA", OwnerMSP: bankA, ParticipantType: models.ParticipantBank})
	invoke(t, eng, cb, contract.OpCreateWallet, models.CreateWalletRequest{WalletID: "WB", OwnerMSP: bankB, ParticipantType: models.ParticipantBank})
}

func TestLifecycle_CommitsAndBalancesAddUp(t *testing.T) {
	eng, svc, mem := newUnified(t)
	ctx := context.Background()

	setupWallets(t, eng)

	_, err := eng.Invoke(ctx, bankA, contract.OpCreateWallet, models.CreateWalletRequest{WalletID: "WX", OwnerMSP: bankA, ParticipantType: models.ParticipantBank}, true)
	expectCode(t, err, contract.ErrAccessDenied)

	invoke(t, eng, cb, contract.OpMint, models.MintCBDCRequest{WalletID: "WA", MovementID: "M1", Amount: 1_000_000, Salt: salt})
	if bv := balance(t, svc, mem, bankA, "WA"); bv.PendingCredits != 1_000_000 || bv.Settled != 0 {
		t.Fatalf("after mint: %+v", bv)
	}

	_, err = eng.Invoke(ctx, cb, contract.OpMint, models.MintCBDCRequest{WalletID: "WA", MovementID: "M1", Amount: 5, Salt: salt}, true)
	expectCode(t, err, contract.ErrDuplicate)

	res := invoke(t, eng, bankA, contract.OpTransfer, models.TransferCBDCRequest{FromWalletID: "WA", ToWalletID: "WB", MovementID: "T1", Amount: 300_000, Salt: salt})
	signers := map[string][]string{}
	for _, e := range res.Endorsements {
		signers[e.Namespace] = append(signers[e.Namespace], e.EndorserMSP)
	}
	if got := signers["cbdc_org1_pdc"]; !slices.Equal(got, []string{bankA, cb}) {
		t.Fatalf("org1 namespace endorsed by %v", got)
	}
	if _, touched := signers["cbdc_org2_pdc"]; touched {
		t.Fatal("transfer must not need the payee bank's endorsement")
	}

	if bv := balance(t, svc, mem, bankA, "WA"); bv.Settled != 700_000 || bv.PendingCredits != 0 {
		t.Fatalf("WA after transfer: %+v", bv)
	}
	if bv := balance(t, svc, mem, bankB, "WB"); bv.PendingCredits != 300_000 {
		t.Fatalf("WB after transfer: %+v", bv)
	}

	invoke(t, eng, bankB, contract.OpConsolidate, models.ConsolidateRequest{WalletID: "WB"})
	if bv := balance(t, svc, mem, bankB, "WB"); bv.Settled != 300_000 || bv.PendingCredits != 0 {
		t.Fatalf("WB after consolidate: %+v", bv)
	}
	_, err = eng.Invoke(ctx, bankB, contract.OpConsolidate, models.ConsolidateRequest{WalletID: "WB"}, true)
	expectCode(t, err, contract.ErrInvalidState)

	_, err = eng.Invoke(ctx, bankB, contract.OpTransfer, models.TransferCBDCRequest{FromWalletID: "WA", ToWalletID: "WB", MovementID: "T9", Amount: 1, Salt: salt}, true)
	expectCode(t, err, contract.ErrAccessDenied)

	_, err = eng.Invoke(ctx, bankA, contract.OpTransfer, models.TransferCBDCRequest{FromWalletID: "WA", ToWalletID: "WB", MovementID: "T2", Amount: 700_001, Salt: salt}, true)
	expectCode(t, err, contract.ErrInsufficient)

	invoke(t, eng, bankA, contract.OpRedeem, models.RedeemCBDCRequest{WalletID: "WA", MovementID: "R1", Amount: 100_000, Salt: salt})
	if bv := balance(t, svc, mem, bankA, "WA"); bv.Settled != 600_000 {
		t.Fatalf("WA after redeem: %+v", bv)
	}
	if got := supply(t, svc, mem); got != 900_000 {
		t.Fatalf("supply = %d, want 900000", got)
	}

	invoke(t, eng, cb, contract.OpFreezeWallet, models.WalletStatusRequest{WalletID: "WB", Reason: "compliance"})
	_, err = eng.Invoke(ctx, bankA, contract.OpTransfer, models.TransferCBDCRequest{FromWalletID: "WA", ToWalletID: "WB", MovementID: "T3", Amount: 1, Salt: salt}, true)
	expectCode(t, err, contract.ErrBlocked)
	invoke(t, eng, cb, contract.OpUnfreezeWallet, models.WalletStatusRequest{WalletID: "WB"})
	invoke(t, eng, bankA, contract.OpTransfer, models.TransferCBDCRequest{FromWalletID: "WA", ToWalletID: "WB", MovementID: "T3", Amount: 1, Salt: salt})

	sim := simulator.NewMultiNamespaceSimulator(ctx, mem)
	if ok, err := svc.VerifyMovementAmount(sim, "T1", 300_000, salt); err != nil || !ok {
		t.Fatalf("verify T1: %v %v", ok, err)
	}
	if ok, _ := svc.VerifyMovementAmount(sim, "T1", 299_999, salt); ok {
		t.Fatal("wrong amount verified")
	}
}

func TestInvoke_WithoutSubmitLeavesLedgerUntouched(t *testing.T) {
	eng, svc, mem := newUnified(t)
	res, err := eng.Invoke(context.Background(), cb, contract.OpCreateWallet,
		models.CreateWalletRequest{WalletID: "WA", OwnerMSP: bankA, ParticipantType: models.ParticipantBank}, false)
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != models.TxStatusEndorsed || len(res.Envelope) == 0 {
		t.Fatalf("expected endorsed envelope, got %+v", res)
	}
	sim := simulator.NewMultiNamespaceSimulator(context.Background(), mem)
	if _, err := svc.QueryWallet(sim, "WA"); err == nil {
		t.Fatal("wallet must not exist before submission")
	}
}

// ─── Multi-organization ─────────────────────────────────────────────────────

type orgNode struct {
	msp    string
	id     *endorser.Identity
	engine *engine.Engine
	server *httptest.Server
}

// newNetwork starts one X-Client per organization. Each holds only its own key and asks
// the others for endorsements over HTTP; all read and commit to one shared ledger.
func newNetwork(t *testing.T) (map[string]*orgNode, *contract.CBDCService, *store.InMemoryStore) {
	t.Helper()
	svc := newService()
	return newNetworkWith(t, svc, engine.DefaultPolicies(svc, banks), cb, bankA, bankB)
}

// newNetworkWith starts one X-Client per MSP in msps with the given contract and policies.
func newNetworkWith(t *testing.T, svc *contract.CBDCService, policies engine.Policies, msps ...string) (map[string]*orgNode, *contract.CBDCService, *store.InMemoryStore) {
	t.Helper()
	mem := store.NewInMemoryStore()

	nodes := map[string]*orgNode{}
	for _, msp := range msps {
		id, err := endorser.Generate(msp)
		if err != nil {
			t.Fatal(err)
		}
		nodes[msp] = &orgNode{msp: msp, id: id}
	}

	// Servers first so every node knows every peer URL.
	endpoints := map[string]string{}
	handlers := map[string]*lazyHandler{}
	for msp, n := range nodes {
		h := &lazyHandler{}
		handlers[msp] = h
		n.server = httptest.NewServer(h)
		t.Cleanup(n.server.Close)
		endpoints[msp] = n.server.URL
	}

	for msp, n := range nodes {
		reg := endorser.NewRegistry(msp)
		reg.Register(n.id)
		verifier, err := peers.NewVerifier(nil)
		if err != nil {
			t.Fatal(err)
		}
		others := map[string]string{}
		for other, on := range nodes {
			if other != msp {
				verifier.TrustRoot(other, on.id.Certificate())
				others[other] = endpoints[other]
			}
		}
		n.engine = engine.New(engine.Config{
			Contract:  svc,
			Store:     mem,
			Registry:  reg,
			Policies:  policies,
			Peers:     peers.NewClient(others, nil),
			Submitter: fabricx.LocalSubmitter{Store: mem},
			Channel:   "mychannel",
		})
		handlers[msp].h = api.NewServer(svc, n.engine, api.Options{DefaultMSP: msp, Verifier: verifier}).Handler()
	}
	return nodes, svc, mem
}

func TestMultiOrg_BanksNeedCentralBankCoSignature(t *testing.T) {
	nodes, svc, mem := newNetwork(t)
	cbNode, a, b := nodes[cb].engine, nodes[bankA].engine, nodes[bankB].engine

	setupWallets(t, cbNode)
	invoke(t, cbNode, cb, contract.OpMint, models.MintCBDCRequest{WalletID: "WA", MovementID: "M1", Amount: 1_000_000, Salt: salt})

	// Bank A initiates; the Central Bank X-Client re-executes and co-signs.
	res := invoke(t, a, bankA, contract.OpTransfer, models.TransferCBDCRequest{FromWalletID: "WA", ToWalletID: "WB", MovementID: "T1", Amount: 250_000, Salt: salt})
	var cbSigned, aSigned bool
	for _, e := range res.Endorsements {
		cbSigned = cbSigned || e.EndorserMSP == cb
		aSigned = aSigned || e.EndorserMSP == bankA
	}
	if !cbSigned || !aSigned {
		t.Fatalf("expected Bank A and Central Bank endorsements, got %+v", res.Endorsements)
	}

	invoke(t, b, bankB, contract.OpConsolidate, models.ConsolidateRequest{WalletID: "WB"})
	if bv := balance(t, svc, mem, bankB, "WB"); bv.Settled != 250_000 {
		t.Fatalf("WB = %+v", bv)
	}

	// Bank B's node cannot act as Bank A.
	_, err := b.Invoke(context.Background(), bankA, contract.OpTransfer, models.TransferCBDCRequest{FromWalletID: "WA", ToWalletID: "WB", MovementID: "T2", Amount: 1, Salt: salt}, true)
	expectCode(t, err, contract.ErrAccessDenied)

	// Bank B's own execution already refuses to debit a wallet it does not own.
	_, err = b.Invoke(context.Background(), bankB, contract.OpTransfer, models.TransferCBDCRequest{FromWalletID: "WA", ToWalletID: "WB", MovementID: "T2", Amount: 1, Salt: salt}, true)
	expectCode(t, err, contract.ErrAccessDenied)
}

func TestMultiOrg_CentralBankRefusesUnauthorisedOrUnauthenticatedRequests(t *testing.T) {
	nodes, _, _ := newNetwork(t)
	setupWallets(t, nodes[cb].engine)
	invoke(t, nodes[cb].engine, cb, contract.OpMint, models.MintCBDCRequest{WalletID: "WA", MovementID: "M1", Amount: 10, Salt: salt})

	client := peers.NewClient(map[string]string{cb: nodes[cb].server.URL}, nil)
	req := peers.EndorseRequest{
		Op:        contract.OpTransfer,
		Request:   []byte(`{"fromWalletId":"WA","toWalletId":"WB","movementId":"T1","amount":5,"salt":"` + salt + `"}`),
		TxID:      "deadbeef",
		Timestamp: time.Now().UTC().Format(time.RFC3339),
	}

	// A genuine Bank B identity asks CB to co-sign a debit of Bank A's wallet: CB's
	// re-execution runs as Bank B and refuses.
	_, err := client.Endorse(context.Background(), cb, nodes[bankB].id, req)
	var perr *peers.Error
	if !errors.As(err, &perr) || perr.Status != http.StatusForbidden || perr.Code != contract.ErrAccessDenied {
		t.Fatalf("expected 403 ACCESS_DENIED from CB, got %v", err)
	}

	// A key claiming to be Bank A but not issued by Bank A's CA is not authenticated.
	impostor, err := endorser.Generate(bankA)
	if err != nil {
		t.Fatal(err)
	}
	_, err = client.Endorse(context.Background(), cb, impostor, req)
	if !errors.As(err, &perr) || perr.Status != http.StatusUnauthorized {
		t.Fatalf("expected 401 for impostor, got %v", err)
	}
}

func TestMultiOrg_FailsWhenCentralBankUnreachable(t *testing.T) {
	nodes, _, _ := newNetwork(t)
	setupWallets(t, nodes[cb].engine)
	invoke(t, nodes[cb].engine, cb, contract.OpMint, models.MintCBDCRequest{WalletID: "WA", MovementID: "M1", Amount: 10, Salt: salt})

	nodes[cb].server.Close()
	_, err := nodes[bankA].engine.Invoke(context.Background(), bankA, contract.OpTransfer,
		models.TransferCBDCRequest{FromWalletID: "WA", ToWalletID: "WB", MovementID: "T1", Amount: 5, Salt: salt}, true)
	if err == nil {
		t.Fatal("transfer must fail without the Central Bank endorsement")
	}
}

func TestEndorseForPeer_RejectsTamperedProposal(t *testing.T) {
	nodes, _, _ := newNetwork(t)
	setupWallets(t, nodes[cb].engine)
	invoke(t, nodes[cb].engine, cb, contract.OpMint, models.MintCBDCRequest{WalletID: "WA", MovementID: "M1", Amount: 10, Salt: salt})

	// Bank A gets an honest endorsement, then edits the values it asks CB to sign again.
	res, err := nodes[bankA].engine.Invoke(context.Background(), bankA, contract.OpTransfer,
		models.TransferCBDCRequest{FromWalletID: "WA", ToWalletID: "WB", MovementID: "T1", Amount: 5, Salt: salt}, false)
	if err != nil {
		t.Fatal(err)
	}
	tampered := res.Namespaces
	for _, ns := range tampered {
		for _, w := range ns.BlindWrites {
			w.Value = append(w.Value, ' ')
		}
	}
	req := &peers.EndorseRequest{
		Op:           contract.OpTransfer,
		Request:      []byte(`{"fromWalletId":"WA","toWalletId":"WB","movementId":"T1","amount":5,"salt":"` + salt + `"}`),
		TxID:         res.TxID,
		Timestamp:    res.Payload.(*models.Movement).Timestamp,
		InitiatorMSP: bankA,
	}
	for _, ns := range tampered {
		raw, err := proto.Marshal(ns)
		if err != nil {
			t.Fatal(err)
		}
		req.Namespaces = append(req.Namespaces, raw)
	}
	_, err = nodes[cb].engine.EndorseForPeer(context.Background(), req)
	if !errors.Is(err, engine.ErrProposalMismatch) {
		t.Fatalf("expected proposal mismatch, got %v", err)
	}
}

// lazyHandler lets servers start before the handler they serve is built.
type lazyHandler struct{ h http.Handler }

func (l *lazyHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) { l.h.ServeHTTP(w, r) }
