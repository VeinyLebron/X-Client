package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"x-client/pkg/contract"
	"x-client/pkg/endorser"
	"x-client/pkg/engine"
	"x-client/pkg/fabricx"
	"x-client/pkg/models"
	"x-client/pkg/store"
)

const salt = "secret-salt-16-chars-min"

func newTestServer(t *testing.T, opts Options) http.Handler {
	t.Helper()
	svc := contract.NewCBDCService(contract.DefaultNamespaces())
	mem := store.NewInMemoryStore()
	reg := endorser.NewRegistry(models.CentralBankMSP)
	for _, msp := range []string{models.CentralBankMSP, "Org1MSP", "Org2MSP"} {
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
		Policies:  engine.DefaultPolicies(svc, []string{"Org1MSP", "Org2MSP"}),
		Submitter: fabricx.LocalSubmitter{Store: mem},
		Channel:   "mychannel",
	})
	if opts.DefaultMSP == "" {
		opts.DefaultMSP = models.CentralBankMSP
	}
	return NewServer(svc, eng, opts).Handler()
}

type call struct {
	method, path, msp string
	body              any
	want              int
}

func do(t *testing.T, h http.Handler, c call) models.EndorsementResponse {
	t.Helper()
	var body bytes.Buffer
	if c.body != nil {
		_ = json.NewEncoder(&body).Encode(c.body)
	}
	req := httptest.NewRequest(c.method, c.path, &body)
	if c.msp != "" {
		req.Header.Set("X-Caller-MSP", c.msp)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != c.want {
		t.Fatalf("%s %s: status %d, want %d: %s", c.method, c.path, rec.Code, c.want, rec.Body.String())
	}
	var resp models.EndorsementResponse
	_ = json.Unmarshal(rec.Body.Bytes(), &resp)
	return resp
}

func TestAPI_SetupGuideFlow(t *testing.T) {
	h := newTestServer(t, Options{AllowCallerHeader: true})
	cb := models.CentralBankMSP

	do(t, h, call{"GET", "/healthz", "", nil, 200})
	r := do(t, h, call{"POST", "/api/v1/wallets", cb, models.CreateWalletRequest{WalletID: "WA", OwnerMSP: "Org1MSP", ParticipantType: "bank"}, 200})
	if r.Status != models.TxStatusCommitted || r.TxID == "" || len(r.Endorsements) == 0 {
		t.Fatalf("create: %+v", r)
	}
	do(t, h, call{"POST", "/api/v1/wallets", cb, models.CreateWalletRequest{WalletID: "WB", OwnerMSP: "Org2MSP", ParticipantType: "bank"}, 200})
	do(t, h, call{"POST", "/api/v1/wallets", cb, models.CreateWalletRequest{WalletID: "WB", OwnerMSP: "Org2MSP", ParticipantType: "bank"}, 409})

	// Committed state is visible immediately.
	if r := do(t, h, call{"GET", "/api/v1/wallets/WA", "", nil, 200}); r.Status != models.TxStatusQueried {
		t.Fatalf("query: %+v", r)
	}

	do(t, h, call{"POST", "/api/v1/movements/mint", cb, models.MintCBDCRequest{WalletID: "WA", MovementID: "M1001", Amount: 1_000_000, Salt: salt}, 200})
	do(t, h, call{"POST", "/api/v1/movements/mint", "Org1MSP", models.MintCBDCRequest{WalletID: "WA", MovementID: "M1002", Amount: 1, Salt: salt}, 403})
	do(t, h, call{"POST", "/api/v1/movements/transfer", "Org1MSP", models.TransferCBDCRequest{FromWalletID: "WA", ToWalletID: "WB", MovementID: "T2001", Amount: 300_000, Salt: salt}, 200})
	do(t, h, call{"POST", "/api/v1/movements/transfer", "Org1MSP", models.TransferCBDCRequest{FromWalletID: "WA", ToWalletID: "WB", MovementID: "T2002", Amount: 900_000, Salt: salt}, 422})
	do(t, h, call{"POST", "/api/v1/wallets/WB/consolidate", "Org2MSP", nil, 200})

	bal := do(t, h, call{"GET", "/api/v1/wallets/WB/balance", "Org2MSP", nil, 200})
	if b := bal.Payload.(map[string]any); b["settled"].(float64) != 300_000 {
		t.Fatalf("WB balance: %v", b)
	}
	do(t, h, call{"GET", "/api/v1/wallets/WB/balance", "Org1MSP", nil, 403})

	v := do(t, h, call{"POST", "/api/v1/movements/T2001/verify", "", models.VerifyMovementRequest{Amount: 300_000, Salt: salt}, 200})
	if v.Payload.(map[string]any)["verified"] != true {
		t.Fatalf("verify: %+v", v)
	}
	sup := do(t, h, call{"GET", "/api/v1/supply", cb, nil, 200})
	if sup.Payload.(map[string]any)["totalSupply"].(float64) != 1_000_000 {
		t.Fatalf("supply: %+v", sup)
	}

	e := do(t, h, call{"POST", "/api/v1/wallets/WA/freeze?submit=false", cb, map[string]string{"reason": "check"}, 200})
	if e.Status != models.TxStatusEndorsed || e.Envelope == "" {
		t.Fatalf("submit=false: %+v", e)
	}
	if w := do(t, h, call{"GET", "/api/v1/wallets/WA", "", nil, 200}); w.Payload.(map[string]any)["status"] != models.WalletActive {
		t.Fatal("submit=false must not change the ledger")
	}
	do(t, h, call{"GET", "/api/v1/movements/NOPE", "", nil, 404})
}

func TestAPI_CallerHeaderAndToken(t *testing.T) {
	h := newTestServer(t, Options{DefaultMSP: "Org1MSP", APIToken: "s3cret"})

	req := httptest.NewRequest("GET", "/api/v1/supply", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("missing token: %d", rec.Code)
	}

	req = httptest.NewRequest("GET", "/api/v1/supply", nil)
	req.Header.Set("Authorization", "Bearer s3cret")
	req.Header.Set("X-Caller-MSP", models.CentralBankMSP)
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("header impersonation must be refused: %d", rec.Code)
	}

	req = httptest.NewRequest("GET", "/healthz", nil)
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("healthz must not require the token: %d", rec.Code)
	}
}
