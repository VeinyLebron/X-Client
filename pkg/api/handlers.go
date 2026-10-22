package api

import (
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"strings"

	"github.com/hyperledger/fabric-x-common/api/committerpb"

	"x-client/pkg/contract"
	"x-client/pkg/engine"
	"x-client/pkg/models"
	"x-client/pkg/peers"
	"x-client/pkg/simulator"
)

// maxBody bounds request body sizes.
const maxBody = 1 << 20

// Options configures the REST server.
type Options struct {
	// DefaultMSP is the identity requests act as.
	DefaultMSP string
	// AllowCallerHeader lets clients pick the acting MSP with X-Caller-MSP. Only safe when one
	// trusted process holds every organization's key (in-memory or single-operator setups).
	AllowCallerHeader bool
	// APIToken, if set, is required as "Authorization: Bearer <token>" on /api routes.
	APIToken string
	// Verifier authenticates peer endorsement requests; nil disables the internal route.
	Verifier *peers.Verifier
	// Ready reports whether the node has caught up with the ledger; nil means always ready.
	Ready func() error
	// Mode is reported by /healthz.
	Mode string
}

// Server handles REST API routing and execution.
type Server struct {
	svc    *contract.CBDCService
	engine *engine.Engine
	opts   Options
	mux    *http.ServeMux
}

func NewServer(svc *contract.CBDCService, eng *engine.Engine, opts Options) *Server {
	s := &Server{svc: svc, engine: eng, opts: opts, mux: http.NewServeMux()}
	s.registerRoutes()
	return s
}

func (s *Server) Handler() http.Handler {
	return s.mux
}

func (s *Server) registerRoutes() {
	// Wallets
	s.handle("POST /api/v1/wallets", s.handleCreateWallet)
	s.handle("POST /api/v1/wallets/{id}/freeze", s.handleFreezeWallet)
	s.handle("POST /api/v1/wallets/{id}/unfreeze", s.handleUnfreezeWallet)
	s.handle("GET /api/v1/wallets/{id}", s.handleQueryWallet)
	s.handle("GET /api/v1/wallets/{id}/balance", s.handleCheckBalance)
	s.handle("POST /api/v1/wallets/{id}/consolidate", s.handleConsolidateBalance)

	// Movements
	s.handle("POST /api/v1/movements/mint", s.handleMintCBDC)
	s.handle("POST /api/v1/movements/transfer", s.handleTransferCBDC)
	s.handle("POST /api/v1/movements/redeem", s.handleRedeemCBDC)
	s.handle("GET /api/v1/movements/{id}", s.handleQueryMovement)
	s.handle("POST /api/v1/movements/{id}/verify", s.handleVerifyMovement)

	// Supply
	s.handle("GET /api/v1/supply", s.handleQueryTotalSupply)

	// Peer endorsement (authenticated by the request signature, not the API token)
	if s.opts.Verifier != nil {
		s.mux.HandleFunc("POST "+peers.EndorsePath, s.handlePeerEndorse)
	}

	s.mux.HandleFunc("GET /healthz", s.handleHealth)
}

// handle registers an /api route behind the optional bearer token.
func (s *Server) handle(pattern string, h http.HandlerFunc) {
	s.mux.HandleFunc(pattern, func(w http.ResponseWriter, r *http.Request) {
		if s.opts.APIToken != "" {
			got := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
			if subtle.ConstantTimeCompare([]byte(got), []byte(s.opts.APIToken)) != 1 {
				writeError(w, http.StatusUnauthorized, "UNAUTHORIZED", "missing or invalid bearer token", "")
				return
			}
		}
		h(w, r)
	})
}

// callerMSP returns the MSP a request acts as.
func (s *Server) callerMSP(w http.ResponseWriter, r *http.Request) (string, bool) {
	hdr := r.Header.Get("X-Caller-MSP")
	if hdr == "" || hdr == s.opts.DefaultMSP {
		return s.opts.DefaultMSP, true
	}
	if !s.opts.AllowCallerHeader {
		writeError(w, http.StatusForbidden, contract.ErrAccessDenied,
			"this X-Client acts only as "+s.opts.DefaultMSP+"; send the request to "+hdr+"'s X-Client", "")
		return "", false
	}
	return hdr, true
}

// ─── Responses ──────────────────────────────────────────────────────────────

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, code, msg, txID string) {
	writeJSON(w, status, models.ErrorResponse{Error: msg, Code: code, TxID: txID})
}

// writeFailure maps an error from the contract, engine or network to an HTTP response.
func writeFailure(w http.ResponseWriter, err error) {
	if ce, ok := contract.AsError(err); ok {
		writeError(w, contractStatus(ce.Code), ce.Code, ce.Msg, "")
		return
	}
	var (
		idErr   *engine.IdentityError
		txErr   *engine.TxError
		peerErr *peers.Error
	)
	switch {
	case errors.As(err, &txErr):
		status := http.StatusBadGateway
		if txErr.Status == committerpb.Status_ABORTED_MVCC_CONFLICT {
			status = http.StatusConflict
		}
		writeError(w, status, txErr.Status.String(), err.Error(), txErr.TxID)
	case errors.As(err, &peerErr):
		switch {
		case peerErr.Code == peers.CodeUnreachable:
			writeError(w, http.StatusServiceUnavailable, peers.CodeUnreachable, err.Error(), "")
		case peerErr.Status >= 400 && peerErr.Status < 500 && peerErr.Code != "":
			// The peer refused on business grounds; pass its verdict through.
			writeError(w, peerErr.Status, peerErr.Code, err.Error(), "")
		default:
			writeError(w, http.StatusBadGateway, "PEER_REJECTED", err.Error(), "")
		}
	case errors.As(err, &idErr):
		writeError(w, http.StatusServiceUnavailable, "ENDORSEMENT_UNAVAILABLE", err.Error(), "")
	case errors.Is(err, engine.ErrProposalMismatch):
		writeError(w, http.StatusConflict, engine.CodeProposalMismatch, err.Error(), "")
	case errors.Is(err, engine.ErrNoStateChange):
		writeError(w, http.StatusUnprocessableEntity, "NO_STATE_CHANGE", err.Error(), "")
	default:
		log.Printf("[API] internal error: %v", err)
		writeError(w, http.StatusInternalServerError, "INTERNAL_ERROR", err.Error(), "")
	}
}

func contractStatus(code string) int {
	switch code {
	case contract.ErrInvalidArgument:
		return http.StatusBadRequest
	case contract.ErrNotFound:
		return http.StatusNotFound
	case contract.ErrAccessDenied:
		return http.StatusForbidden
	case contract.ErrAlreadyExists, contract.ErrDuplicate:
		return http.StatusConflict
	default: // INVALID_STATE, ACCOUNT_BLOCKED, INSUFFICIENT_BALANCE
		return http.StatusUnprocessableEntity
	}
}

func toResponse(res *engine.Result) models.EndorsementResponse {
	out := models.EndorsementResponse{
		TxID:         res.TxID,
		Status:       res.Status,
		Payload:      res.Payload,
		ReadWriteSet: simulator.View(res.Namespaces),
		Endorsements: res.Endorsements,
	}
	if len(res.Envelope) > 0 {
		out.Envelope = base64.StdEncoding.EncodeToString(res.Envelope)
	}
	return out
}

// ─── Mutations ──────────────────────────────────────────────────────────────

// invoke runs a mutating operation. ?submit=false returns the signed envelope instead of
// ordering it.
func (s *Server) invoke(w http.ResponseWriter, r *http.Request, op string, req any) {
	caller, ok := s.callerMSP(w, r)
	if !ok {
		return
	}
	submit := r.URL.Query().Get("submit") != "false"
	res, err := s.engine.Invoke(r.Context(), caller, op, req, submit)
	if err != nil {
		writeFailure(w, err)
		return
	}
	code := http.StatusOK
	if res.Status == models.TxStatusSubmitted {
		code = http.StatusAccepted
	}
	writeJSON(w, code, toResponse(res))
}

// decodeBody decodes an optional JSON body into v.
func decodeBody(w http.ResponseWriter, r *http.Request, v any) bool {
	err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBody)).Decode(v)
	if err != nil && !errors.Is(err, io.EOF) {
		writeError(w, http.StatusBadRequest, contract.ErrInvalidArgument, "invalid JSON body: "+err.Error(), "")
		return false
	}
	return true
}

func (s *Server) handleCreateWallet(w http.ResponseWriter, r *http.Request) {
	var req models.CreateWalletRequest
	if decodeBody(w, r, &req) {
		s.invoke(w, r, contract.OpCreateWallet, req)
	}
}

func (s *Server) handleFreezeWallet(w http.ResponseWriter, r *http.Request) {
	var req models.WalletStatusRequest
	if decodeBody(w, r, &req) {
		req.WalletID = r.PathValue("id")
		s.invoke(w, r, contract.OpFreezeWallet, req)
	}
}

func (s *Server) handleUnfreezeWallet(w http.ResponseWriter, r *http.Request) {
	s.invoke(w, r, contract.OpUnfreezeWallet, models.WalletStatusRequest{WalletID: r.PathValue("id")})
}

func (s *Server) handleConsolidateBalance(w http.ResponseWriter, r *http.Request) {
	s.invoke(w, r, contract.OpConsolidate, models.ConsolidateRequest{WalletID: r.PathValue("id")})
}

func (s *Server) handleMintCBDC(w http.ResponseWriter, r *http.Request) {
	var req models.MintCBDCRequest
	if decodeBody(w, r, &req) {
		s.invoke(w, r, contract.OpMint, req)
	}
}

func (s *Server) handleTransferCBDC(w http.ResponseWriter, r *http.Request) {
	var req models.TransferCBDCRequest
	if decodeBody(w, r, &req) {
		s.invoke(w, r, contract.OpTransfer, req)
	}
}

func (s *Server) handleRedeemCBDC(w http.ResponseWriter, r *http.Request) {
	var req models.RedeemCBDCRequest
	if decodeBody(w, r, &req) {
		s.invoke(w, r, contract.OpRedeem, req)
	}
}

// ─── Queries ────────────────────────────────────────────────────────────────

func (s *Server) query(w http.ResponseWriter, r *http.Request, fn func(*simulator.MultiNamespaceSimulator) (any, error)) {
	res, err := s.engine.Query(r.Context(), fn)
	if err != nil {
		writeFailure(w, err)
		return
	}
	writeJSON(w, http.StatusOK, toResponse(res))
}

func (s *Server) handleQueryWallet(w http.ResponseWriter, r *http.Request) {
	s.query(w, r, func(sim *simulator.MultiNamespaceSimulator) (any, error) {
		return s.svc.QueryWallet(sim, r.PathValue("id"))
	})
}

func (s *Server) handleCheckBalance(w http.ResponseWriter, r *http.Request) {
	caller, ok := s.callerMSP(w, r)
	if !ok {
		return
	}
	s.query(w, r, func(sim *simulator.MultiNamespaceSimulator) (any, error) {
		return s.svc.CheckBalance(sim, caller, r.PathValue("id"))
	})
}

func (s *Server) handleQueryMovement(w http.ResponseWriter, r *http.Request) {
	s.query(w, r, func(sim *simulator.MultiNamespaceSimulator) (any, error) {
		return s.svc.QueryMovement(sim, r.PathValue("id"))
	})
}

func (s *Server) handleVerifyMovement(w http.ResponseWriter, r *http.Request) {
	var req models.VerifyMovementRequest
	if !decodeBody(w, r, &req) {
		return
	}
	id := r.PathValue("id")
	s.query(w, r, func(sim *simulator.MultiNamespaceSimulator) (any, error) {
		ok, err := s.svc.VerifyMovementAmount(sim, id, req.Amount, req.Salt)
		if err != nil {
			return nil, err
		}
		return map[string]any{"movementId": id, "verified": ok}, nil
	})
}

func (s *Server) handleQueryTotalSupply(w http.ResponseWriter, r *http.Request) {
	caller, ok := s.callerMSP(w, r)
	if !ok {
		return
	}
	s.query(w, r, func(sim *simulator.MultiNamespaceSimulator) (any, error) {
		return s.svc.QueryTotalSupply(sim, caller)
	})
}

// ─── Peer endorsement & health ──────────────────────────────────────────────

func (s *Server) handlePeerEndorse(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 4<<20))
	if err != nil {
		writeError(w, http.StatusBadRequest, contract.ErrInvalidArgument, err.Error(), "")
		return
	}
	req, err := s.opts.Verifier.Verify(body, r.Header.Get(peers.SignatureHeader))
	if err != nil {
		log.Printf("[Endorse] rejected unauthenticated request: %v", err)
		writeError(w, http.StatusUnauthorized, "UNAUTHENTICATED", err.Error(), "")
		return
	}
	resp, err := s.engine.EndorseForPeer(r.Context(), req)
	if err != nil {
		log.Printf("[Endorse] %s op=%s for %s refused: %v", req.TxID, req.Op, req.InitiatorMSP, err)
		writeFailure(w, err)
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

func (s *Server) handleHealth(w http.ResponseWriter, _ *http.Request) {
	body := map[string]any{
		"status":          "UP",
		"mode":            s.opts.Mode,
		"defaultEndorser": s.opts.DefaultMSP,
		"ready":           true,
	}
	code := http.StatusOK
	if s.opts.Ready != nil {
		if err := s.opts.Ready(); err != nil {
			body["ready"] = false
			body["readyError"] = err.Error()
			code = http.StatusServiceUnavailable
		}
	}
	writeJSON(w, code, body)
}
