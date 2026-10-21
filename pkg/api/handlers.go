package api

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"strings"

	"x-client/pkg/contract"
	"x-client/pkg/endorser"
	"x-client/pkg/models"
	"x-client/pkg/simulator"
	"x-client/pkg/store"
)

// Server handles REST API routing and execution.
type Server struct {
	svc      *contract.CBDCService
	store    store.Store
	registry *endorser.Registry
	mux      *http.ServeMux
}

func NewServer(svc *contract.CBDCService, st store.Store, reg *endorser.Registry) *Server {
	s := &Server{
		svc:      svc,
		store:    st,
		registry: reg,
		mux:      http.NewServeMux(),
	}
	s.registerRoutes()
	return s
}

func (s *Server) Handler() http.Handler {
	return s.mux
}

func (s *Server) getCallerMSP(r *http.Request) string {
	msp := r.Header.Get("X-Caller-MSP")
	if msp != "" {
		return msp
	}
	return s.registry.DefaultMSPID()
}

func newTxID() string {
	var b [24]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, code int, err error) {
	errStr := err.Error()
	parts := strings.SplitN(errStr, ": ", 2)
	var errCode, msg string
	if len(parts) == 2 {
		errCode = parts[0]
		msg = parts[1]
	} else {
		errCode = "INTERNAL_ERROR"
		msg = errStr
	}

	writeJSON(w, code, models.ErrorResponse{
		Error: msg,
		Code:  errCode,
	})
}

func (s *Server) registerRoutes() {
	// Wallets
	s.mux.HandleFunc("POST /api/v1/wallets", s.handleCreateWallet)
	s.mux.HandleFunc("POST /api/v1/wallets/{id}/freeze", s.handleFreezeWallet)
	s.mux.HandleFunc("POST /api/v1/wallets/{id}/unfreeze", s.handleUnfreezeWallet)
	s.mux.HandleFunc("GET /api/v1/wallets/{id}", s.handleQueryWallet)
	s.mux.HandleFunc("GET /api/v1/wallets/{id}/balance", s.handleCheckBalance)
	s.mux.HandleFunc("POST /api/v1/wallets/{id}/consolidate", s.handleConsolidateBalance)

	// Movements
	s.mux.HandleFunc("POST /api/v1/movements/mint", s.handleMintCBDC)
	s.mux.HandleFunc("POST /api/v1/movements/transfer", s.handleTransferCBDC)
	s.mux.HandleFunc("POST /api/v1/movements/redeem", s.handleRedeemCBDC)
	s.mux.HandleFunc("GET /api/v1/movements/{id}", s.handleQueryMovement)
	s.mux.HandleFunc("POST /api/v1/movements/{id}/verify", s.handleVerifyMovement)

	// Supply
	s.mux.HandleFunc("GET /api/v1/supply", s.handleQueryTotalSupply)

	// Health check
	s.mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{
			"status":        "UP",
			"defaultEndorser": s.registry.DefaultMSPID(),
		})
	})
}

// ─── Wallet Handlers ─────────────────────────────────────────────────────────

func (s *Server) handleCreateWallet(w http.ResponseWriter, r *http.Request) {
	var req models.CreateWalletRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}

	callerMSP := s.getCallerMSP(r)
	txID := newTxID()
	sim := simulator.NewMultiNamespaceSimulator(r.Context(), s.store)

	wallet, err := s.svc.CreateWallet(sim, callerMSP, txID, req)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}

	rwSets, err := sim.BuildReadWriteSets()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}

	sig, err := s.registry.Get(callerMSP).SignReadWriteSet(rwSets)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}

	writeJSON(w, http.StatusOK, models.EndorsementResponse{
		TxID:         txID,
		Status:       "ENDORSED",
		Payload:      wallet,
		ReadWriteSet: rwSets,
		Endorsement:  sig,
	})
}

func (s *Server) handleFreezeWallet(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var req models.FreezeWalletRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}

	callerMSP := s.getCallerMSP(r)
	txID := newTxID()
	sim := simulator.NewMultiNamespaceSimulator(r.Context(), s.store)

	wallet, err := s.svc.FreezeWallet(sim, callerMSP, id, req.Reason)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}

	rwSets, err := sim.BuildReadWriteSets()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}

	sig, err := s.registry.Get(callerMSP).SignReadWriteSet(rwSets)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}

	writeJSON(w, http.StatusOK, models.EndorsementResponse{
		TxID:         txID,
		Status:       "ENDORSED",
		Payload:      wallet,
		ReadWriteSet: rwSets,
		Endorsement:  sig,
	})
}

func (s *Server) handleUnfreezeWallet(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	callerMSP := s.getCallerMSP(r)
	txID := newTxID()
	sim := simulator.NewMultiNamespaceSimulator(r.Context(), s.store)

	wallet, err := s.svc.UnfreezeWallet(sim, callerMSP, id)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}

	rwSets, err := sim.BuildReadWriteSets()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}

	sig, err := s.registry.Get(callerMSP).SignReadWriteSet(rwSets)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}

	writeJSON(w, http.StatusOK, models.EndorsementResponse{
		TxID:         txID,
		Status:       "ENDORSED",
		Payload:      wallet,
		ReadWriteSet: rwSets,
		Endorsement:  sig,
	})
}

func (s *Server) handleQueryWallet(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	sim := simulator.NewMultiNamespaceSimulator(r.Context(), s.store)

	wallet, err := s.svc.QueryWallet(sim, id)
	if err != nil {
		writeError(w, http.StatusNotFound, err)
		return
	}

	rwSets, _ := sim.BuildReadWriteSets()
	writeJSON(w, http.StatusOK, models.EndorsementResponse{
		TxID:         newTxID(),
		Status:       "QUERIED",
		Payload:      wallet,
		ReadWriteSet: rwSets,
	})
}

func (s *Server) handleCheckBalance(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	callerMSP := s.getCallerMSP(r)
	sim := simulator.NewMultiNamespaceSimulator(r.Context(), s.store)

	bal, err := s.svc.CheckBalance(sim, callerMSP, id)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}

	rwSets, _ := sim.BuildReadWriteSets()
	writeJSON(w, http.StatusOK, models.EndorsementResponse{
		TxID:         newTxID(),
		Status:       "QUERIED",
		Payload:      bal,
		ReadWriteSet: rwSets,
	})
}

func (s *Server) handleConsolidateBalance(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	callerMSP := s.getCallerMSP(r)
	txID := newTxID()
	sim := simulator.NewMultiNamespaceSimulator(r.Context(), s.store)

	newBal, err := s.svc.ConsolidateBalance(sim, callerMSP, txID, id)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}

	rwSets, err := sim.BuildReadWriteSets()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}

	sig, err := s.registry.Get(callerMSP).SignReadWriteSet(rwSets)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}

	writeJSON(w, http.StatusOK, models.EndorsementResponse{
		TxID:         txID,
		Status:       "ENDORSED",
		Payload:      map[string]any{"walletId": id, "consolidatedBalance": newBal},
		ReadWriteSet: rwSets,
		Endorsement:  sig,
	})
}

// ─── Movement Handlers ───────────────────────────────────────────────────────

func (s *Server) handleMintCBDC(w http.ResponseWriter, r *http.Request) {
	var req models.MintCBDCRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}

	callerMSP := s.getCallerMSP(r)
	txID := newTxID()
	sim := simulator.NewMultiNamespaceSimulator(r.Context(), s.store)

	mv, err := s.svc.MintCBDC(sim, callerMSP, txID, req)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}

	rwSets, err := sim.BuildReadWriteSets()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}

	sig, err := s.registry.Get(callerMSP).SignReadWriteSet(rwSets)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}

	writeJSON(w, http.StatusOK, models.EndorsementResponse{
		TxID:         txID,
		Status:       "ENDORSED",
		Payload:      mv,
		ReadWriteSet: rwSets,
		Endorsement:  sig,
	})
}

func (s *Server) handleTransferCBDC(w http.ResponseWriter, r *http.Request) {
	var req models.TransferCBDCRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}

	callerMSP := s.getCallerMSP(r)
	txID := newTxID()
	sim := simulator.NewMultiNamespaceSimulator(r.Context(), s.store)

	mv, err := s.svc.TransferCBDC(sim, callerMSP, txID, req)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}

	rwSets, err := sim.BuildReadWriteSets()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}

	sig, err := s.registry.Get(callerMSP).SignReadWriteSet(rwSets)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}

	writeJSON(w, http.StatusOK, models.EndorsementResponse{
		TxID:         txID,
		Status:       "ENDORSED",
		Payload:      mv,
		ReadWriteSet: rwSets,
		Endorsement:  sig,
	})
}

func (s *Server) handleRedeemCBDC(w http.ResponseWriter, r *http.Request) {
	var req models.RedeemCBDCRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}

	callerMSP := s.getCallerMSP(r)
	txID := newTxID()
	sim := simulator.NewMultiNamespaceSimulator(r.Context(), s.store)

	mv, err := s.svc.RedeemCBDC(sim, callerMSP, txID, req)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}

	rwSets, err := sim.BuildReadWriteSets()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}

	sig, err := s.registry.Get(callerMSP).SignReadWriteSet(rwSets)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}

	writeJSON(w, http.StatusOK, models.EndorsementResponse{
		TxID:         txID,
		Status:       "ENDORSED",
		Payload:      mv,
		ReadWriteSet: rwSets,
		Endorsement:  sig,
	})
}

func (s *Server) handleQueryMovement(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	sim := simulator.NewMultiNamespaceSimulator(r.Context(), s.store)

	mv, err := s.svc.QueryMovement(sim, id)
	if err != nil {
		writeError(w, http.StatusNotFound, err)
		return
	}

	rwSets, _ := sim.BuildReadWriteSets()
	writeJSON(w, http.StatusOK, models.EndorsementResponse{
		TxID:         newTxID(),
		Status:       "QUERIED",
		Payload:      mv,
		ReadWriteSet: rwSets,
	})
}

func (s *Server) handleVerifyMovement(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var req models.VerifyMovementRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}

	sim := simulator.NewMultiNamespaceSimulator(r.Context(), s.store)
	verified, err := s.svc.VerifyMovementAmount(sim, id, req.Amount, req.Salt)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"movementId": id,
		"verified":   verified,
	})
}

func (s *Server) handleQueryTotalSupply(w http.ResponseWriter, r *http.Request) {
	callerMSP := s.getCallerMSP(r)
	sim := simulator.NewMultiNamespaceSimulator(r.Context(), s.store)

	supply, err := s.svc.QueryTotalSupply(sim, callerMSP)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}

	rwSets, _ := sim.BuildReadWriteSets()
	writeJSON(w, http.StatusOK, models.EndorsementResponse{
		TxID:         newTxID(),
		Status:       "QUERIED",
		Payload:      supply,
		ReadWriteSet: rwSets,
	})
}
