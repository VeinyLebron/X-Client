package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"x-client/pkg/contract"
	"x-client/pkg/endorser"
	"x-client/pkg/models"
	"x-client/pkg/store"
)

func setupTestServer(t *testing.T) (*Server, *store.InMemoryStore) {
	svc := contract.NewCBDCService()
	st := store.NewInMemoryStore()
	reg := endorser.NewRegistry(models.CentralBankMSP)
	server := NewServer(svc, st, reg)
	return server, st
}

func TestAPI_WalletAndMovementFlow(t *testing.T) {
	server, st := setupTestServer(t)
	handler := server.Handler()

	// 1. Health check
	{
		req := httptest.NewRequest("GET", "/healthz", nil)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("health check failed: %d", rec.Code)
		}
	}

	// 2. Create Wallet WA via POST /api/v1/wallets
	{
		body, _ := json.Marshal(models.CreateWalletRequest{
			WalletID:        "WA",
			OwnerMSP:        "Org1MSP",
			ParticipantType: models.ParticipantBank,
		})
		req := httptest.NewRequest("POST", "/api/v1/wallets", bytes.NewReader(body))
		req.Header.Set("X-Caller-MSP", models.CentralBankMSP)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("create wallet WA status %d: %s", rec.Code, rec.Body.String())
		}

		var resp models.EndorsementResponse
		if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
			t.Fatalf("decode resp: %v", err)
		}
		if resp.Status != "ENDORSED" || resp.Endorsement == nil {
			t.Fatalf("invalid endorsement response: %+v", resp)
		}

		// Save state in in-memory store so subsequent requests see it
		st.Set(models.DefaultPublicNamespace, "WALLET_WA", []byte(`{"walletId":"WA","ownerMsp":"Org1MSP","status":"ACTIVE"}`))
		st.Set(models.PDCNameForMSP("Org1MSP"), "BAL_WA", []byte(`{"walletId":"WA","balance":0}`))
	}

	// 3. Create Wallet WB
	{
		st.Set(models.DefaultPublicNamespace, "WALLET_WB", []byte(`{"walletId":"WB","ownerMsp":"Org2MSP","status":"ACTIVE"}`))
		st.Set(models.PDCNameForMSP("Org2MSP"), "BAL_WB", []byte(`{"walletId":"WB","balance":0}`))
	}

	// 4. Query Wallet WA via GET /api/v1/wallets/WA
	{
		req := httptest.NewRequest("GET", "/api/v1/wallets/WA", nil)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("query wallet WA status %d", rec.Code)
		}
		var resp models.EndorsementResponse
		_ = json.NewDecoder(rec.Body).Decode(&resp)
		if resp.Status != "QUERIED" {
			t.Fatalf("expected QUERIED status, got %s", resp.Status)
		}
	}

	// 5. Mint 500,000 via POST /api/v1/movements/mint
	{
		body, _ := json.Marshal(models.MintCBDCRequest{
			WalletID:   "WA",
			MovementID: "M100",
			Amount:     500000,
			Salt:       "test-salt-16-characters-long",
		})
		req := httptest.NewRequest("POST", "/api/v1/movements/mint", bytes.NewReader(body))
		req.Header.Set("X-Caller-MSP", models.CentralBankMSP)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("mint status %d: %s", rec.Code, rec.Body.String())
		}
		var resp models.EndorsementResponse
		_ = json.NewDecoder(rec.Body).Decode(&resp)
		if _, ok := resp.ReadWriteSet[models.DefaultPublicNamespace]; !ok {
			t.Fatal("expected public namespace write in mint")
		}

		// Update store with minted credit
		st.Set(models.PDCNameForMSP("Org1MSP"), "CREDIT~WA~M100", []byte(`{"walletId":"WA","movementId":"M100","amount":500000}`))
		st.Set(models.DefaultCentralBankPDC, "SUPPLY~M100", []byte(`{"movementId":"M100","delta":500000}`))
	}

	// 6. Check Balance via GET /api/v1/wallets/WA/balance
	{
		req := httptest.NewRequest("GET", "/api/v1/wallets/WA/balance", nil)
		req.Header.Set("X-Caller-MSP", "Org1MSP")
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("check balance status %d", rec.Code)
		}
	}

	// 7. Consolidate Balance via POST /api/v1/wallets/WA/consolidate
	{
		req := httptest.NewRequest("POST", "/api/v1/wallets/WA/consolidate", nil)
		req.Header.Set("X-Caller-MSP", "Org1MSP")
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("consolidate status %d: %s", rec.Code, rec.Body.String())
		}
	}

	// 8. Query Total Supply via GET /api/v1/supply
	{
		req := httptest.NewRequest("GET", "/api/v1/supply", nil)
		req.Header.Set("X-Caller-MSP", models.CentralBankMSP)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("query supply status %d", rec.Code)
		}
	}
}
