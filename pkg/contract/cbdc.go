// Package contract implements the wholesale CBDC rules. Every function is deterministic
// given its TxContext, so independent endorsers re-executing an operation derive
// byte-identical read/write sets.
package contract

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"x-client/pkg/models"
	"x-client/pkg/simulator"
)

// Error codes matching the chaincode.
const (
	ErrInvalidArgument = "INVALID_ARGUMENT"
	ErrNotFound        = "NOT_FOUND"
	ErrAlreadyExists   = "ALREADY_EXISTS"
	ErrAccessDenied    = "ACCESS_DENIED"
	ErrInvalidState    = "INVALID_STATE"
	ErrInsufficient    = "INSUFFICIENT_BALANCE"
	ErrBlocked         = "ACCOUNT_BLOCKED"
	ErrDuplicate       = "DUPLICATE_REQUEST"
)

// Error is a business-rule violation. Any other error returned by the contract is an
// infrastructure failure (e.g. the Query Service is unreachable).
type Error struct {
	Code string
	Msg  string
}

func (e *Error) Error() string { return e.Code + ": " + e.Msg }

func fail(code, format string, args ...any) error {
	return &Error{Code: code, Msg: fmt.Sprintf(format, args...)}
}

// AsError returns the contract error wrapped in err, if any.
func AsError(err error) (*Error, bool) {
	var ce *Error
	ok := errors.As(err, &ce)
	return ce, ok
}

// TxContext carries the inputs a contract call must not derive itself, so that every
// endorser executing the same transaction computes the same result.
type TxContext struct {
	TxID      string
	Timestamp string // RFC 3339, UTC
	CallerMSP string
}

// Mutating operations, as named in peer endorsement requests.
const (
	OpCreateWallet   = "create_wallet"
	OpFreezeWallet   = "freeze_wallet"
	OpUnfreezeWallet = "unfreeze_wallet"
	OpMint           = "mint"
	OpTransfer       = "transfer"
	OpRedeem         = "redeem"
	OpConsolidate    = "consolidate"
)

func hashOf(parts ...string) string {
	sum := sha256.Sum256([]byte(strings.Join(parts, "|")))
	return hex.EncodeToString(sum[:])
}

func amountHash(movementID string, amount int64, salt string) string {
	return hashOf(movementID, strconv.FormatInt(amount, 10), salt)
}

func walletKey(id string) string     { return models.PrefixWallet + id }
func movementKey(id string) string   { return models.PrefixMovement + id }
func balanceKey(id string) string    { return models.PrefixBalance + id }
func moveDetailKey(id string) string { return models.PrefixMoveDetail + id }
func supplyKey(id string) string     { return models.PrefixSupply + id }
func creditPrefix(wallet string) string {
	return models.PrefixCredit + wallet + "~"
}
func creditKey(toWallet, movementID string) string {
	return creditPrefix(toWallet) + movementID
}

// Namespaces names the Fabric-X namespaces the contract uses and the issuing MSP.
type Namespaces struct {
	// CentralBankMSP is the issuer: it creates and freezes wallets, mints, and reads total
	// supply. It may also be a bank (listed in Banks), as Org1 is on the 2-org network.
	CentralBankMSP string
	Public         string
	Supply         string            // supply ledger, and issuer balances unless it is in Banks
	Credits        string            // pending incoming credits for all wallets
	Banks          map[string]string // MSP ID -> balance namespace; defaults to models.PDCNameForMSP
}

// DefaultNamespaces returns the namespace layout registered by devnet-3org.
func DefaultNamespaces() Namespaces {
	return Namespaces{
		CentralBankMSP: models.CentralBankMSP,
		Public:         models.DefaultPublicNamespace,
		Supply:         models.DefaultCentralBankPDC,
		Credits:        models.DefaultCreditsNamespace,
	}
}

// CBDCService implements wholesale CBDC smart contract rules.
type CBDCService struct {
	ns Namespaces
}

func NewCBDCService(ns Namespaces) *CBDCService {
	return &CBDCService{ns: ns}
}

// Namespaces returns the namespace layout in use.
func (c *CBDCService) Namespaces() Namespaces { return c.ns }

// BalanceNamespace returns the namespace holding balances owned by mspID. A bank's own
// namespace wins over the issuer's supply namespace when the issuer is also a bank.
func (c *CBDCService) BalanceNamespace(mspID string) string {
	if ns, ok := c.ns.Banks[mspID]; ok {
		return ns
	}
	if mspID == c.ns.CentralBankMSP {
		return c.ns.Supply
	}
	return models.PDCNameForMSP(mspID)
}

// Execute runs a mutating operation from its JSON request. It is the single entry point
// used both by the initiating endorser and by peers re-executing a proposal.
func (c *CBDCService) Execute(sim *simulator.MultiNamespaceSimulator, tx TxContext, op string, req json.RawMessage) (any, error) {
	switch op {
	case OpCreateWallet:
		var r models.CreateWalletRequest
		if err := decode(req, &r); err != nil {
			return nil, err
		}
		return c.CreateWallet(sim, tx, r)
	case OpFreezeWallet:
		var r models.WalletStatusRequest
		if err := decode(req, &r); err != nil {
			return nil, err
		}
		return c.FreezeWallet(sim, tx, r)
	case OpUnfreezeWallet:
		var r models.WalletStatusRequest
		if err := decode(req, &r); err != nil {
			return nil, err
		}
		return c.UnfreezeWallet(sim, tx, r)
	case OpMint:
		var r models.MintCBDCRequest
		if err := decode(req, &r); err != nil {
			return nil, err
		}
		return c.MintCBDC(sim, tx, r)
	case OpTransfer:
		var r models.TransferCBDCRequest
		if err := decode(req, &r); err != nil {
			return nil, err
		}
		return c.TransferCBDC(sim, tx, r)
	case OpRedeem:
		var r models.RedeemCBDCRequest
		if err := decode(req, &r); err != nil {
			return nil, err
		}
		return c.RedeemCBDC(sim, tx, r)
	case OpConsolidate:
		var r models.ConsolidateRequest
		if err := decode(req, &r); err != nil {
			return nil, err
		}
		bal, err := c.ConsolidateBalance(sim, tx, r.WalletID)
		if err != nil {
			return nil, err
		}
		return map[string]any{"walletId": r.WalletID, "consolidatedBalance": bal}, nil
	default:
		return nil, fail(ErrInvalidArgument, "unknown operation %q", op)
	}
}

func decode(raw json.RawMessage, v any) error {
	if err := json.Unmarshal(raw, v); err != nil {
		return fail(ErrInvalidArgument, "invalid request body: %v", err)
	}
	return nil
}

func (c *CBDCService) requireCentralBank(callerMSP string) error {
	if callerMSP != c.ns.CentralBankMSP {
		return fail(ErrAccessDenied, "caller MSP %s is not authorised (allowed: %s)", callerMSP, c.ns.CentralBankMSP)
	}
	return nil
}

func validateAmount(amount int64, salt string) error {
	if amount <= 0 {
		return fail(ErrInvalidArgument, "amount must be > 0 (got %d)", amount)
	}
	if len(salt) < 16 {
		return fail(ErrInvalidArgument, "salt must be at least 16 characters")
	}
	return nil
}

// ─── Wallets ─────────────────────────────────────────────────────────────────

// CreateWallet registers a wallet. Its balance record is created on first consolidation,
// so wallet creation needs only the Central Bank's endorsement.
func (c *CBDCService) CreateWallet(sim *simulator.MultiNamespaceSimulator, tx TxContext, req models.CreateWalletRequest) (*models.Wallet, error) {
	if err := c.requireCentralBank(tx.CallerMSP); err != nil {
		return nil, err
	}
	if req.WalletID == "" || req.OwnerMSP == "" {
		return nil, fail(ErrInvalidArgument, "walletId and ownerMsp must not be empty")
	}
	if req.ParticipantType != models.ParticipantBank && req.ParticipantType != models.ParticipantCentralBank {
		return nil, fail(ErrInvalidArgument, "participantType must be one of bank, central_bank")
	}
	if req.ParticipantType == models.ParticipantCentralBank && req.OwnerMSP != c.ns.CentralBankMSP {
		return nil, fail(ErrInvalidArgument, "participantType central_bank is reserved for %s", c.ns.CentralBankMSP)
	}
	if req.ParticipantType == models.ParticipantBank && req.OwnerMSP == c.ns.CentralBankMSP && c.ns.Banks[req.OwnerMSP] == "" {
		return nil, fail(ErrInvalidArgument, "%s is not configured as a bank; use participantType central_bank", req.OwnerMSP)
	}

	existing, err := sim.Get(c.ns.Public, walletKey(req.WalletID))
	if err != nil {
		return nil, err
	}
	if len(existing) > 0 {
		return nil, fail(ErrAlreadyExists, "wallet %s already exists", req.WalletID)
	}

	w := &models.Wallet{
		DocType:         "wholesaleWallet",
		WalletID:        req.WalletID,
		OwnerMSP:        req.OwnerMSP,
		ParticipantType: req.ParticipantType,
		Status:          models.WalletActive,
		CreatedAt:       tx.Timestamp,
		UpdatedAt:       tx.Timestamp,
	}
	if err := sim.PutJSON(c.ns.Public, walletKey(req.WalletID), w); err != nil {
		return nil, err
	}
	return w, nil
}

func (c *CBDCService) FreezeWallet(sim *simulator.MultiNamespaceSimulator, tx TxContext, req models.WalletStatusRequest) (*models.Wallet, error) {
	if err := c.requireCentralBank(tx.CallerMSP); err != nil {
		return nil, err
	}
	if req.Reason == "" {
		return nil, fail(ErrInvalidArgument, "reason must not be empty")
	}
	return c.setWalletStatus(sim, tx, req.WalletID, models.WalletFrozen, req.Reason)
}

func (c *CBDCService) UnfreezeWallet(sim *simulator.MultiNamespaceSimulator, tx TxContext, req models.WalletStatusRequest) (*models.Wallet, error) {
	if err := c.requireCentralBank(tx.CallerMSP); err != nil {
		return nil, err
	}
	return c.setWalletStatus(sim, tx, req.WalletID, models.WalletActive, "")
}

func (c *CBDCService) setWalletStatus(sim *simulator.MultiNamespaceSimulator, tx TxContext, walletID, status, reason string) (*models.Wallet, error) {
	w, err := c.getWallet(sim, walletID)
	if err != nil {
		return nil, err
	}
	if w.Status == status {
		return nil, fail(ErrInvalidState, "wallet %s is already %s", walletID, status)
	}

	w.Status = status
	w.FreezeReason = reason
	w.UpdatedAt = tx.Timestamp
	if err := sim.PutJSON(c.ns.Public, walletKey(walletID), w); err != nil {
		return nil, err
	}
	return w, nil
}

// ─── Movements ───────────────────────────────────────────────────────────────

// MintCBDC issues new CBDC as a pending credit to the payee's wallet.
func (c *CBDCService) MintCBDC(sim *simulator.MultiNamespaceSimulator, tx TxContext, req models.MintCBDCRequest) (*models.Movement, error) {
	if err := c.requireCentralBank(tx.CallerMSP); err != nil {
		return nil, err
	}
	if err := validateAmount(req.Amount, req.Salt); err != nil {
		return nil, err
	}
	if err := c.requireNewMovement(sim, req.MovementID); err != nil {
		return nil, err
	}
	if _, err := c.getActiveWallet(sim, req.WalletID); err != nil {
		return nil, err
	}

	credit := &models.PrivateCredit{
		WalletID:   req.WalletID,
		MovementID: req.MovementID,
		Type:       models.MoveMint,
		Amount:     req.Amount,
		Salt:       req.Salt,
		Timestamp:  tx.Timestamp,
	}
	if err := sim.PutJSON(c.ns.Credits, creditKey(req.WalletID, req.MovementID), credit); err != nil {
		return nil, err
	}

	delta := &models.SupplyDelta{MovementID: req.MovementID, Delta: req.Amount, Timestamp: tx.Timestamp}
	if err := sim.PutJSON(c.ns.Supply, supplyKey(req.MovementID), delta); err != nil {
		return nil, err
	}

	return c.putMovement(sim, tx, req.MovementID, models.MoveMint, "", req.WalletID, req.Amount, req.Salt)
}

// TransferCBDC debits the caller's wallet and credits the payee as a pending credit.
func (c *CBDCService) TransferCBDC(sim *simulator.MultiNamespaceSimulator, tx TxContext, req models.TransferCBDCRequest) (*models.Movement, error) {
	if req.FromWalletID == req.ToWalletID {
		return nil, fail(ErrInvalidArgument, "source and destination wallets must differ")
	}
	if err := validateAmount(req.Amount, req.Salt); err != nil {
		return nil, err
	}
	if err := c.requireNewMovement(sim, req.MovementID); err != nil {
		return nil, err
	}

	fromWallet, err := c.getOwnedActiveWallet(sim, tx.CallerMSP, req.FromWalletID)
	if err != nil {
		return nil, err
	}
	if _, err := c.getActiveWallet(sim, req.ToWalletID); err != nil {
		return nil, err
	}

	if err := c.debit(sim, tx, fromWallet, req.Amount); err != nil {
		return nil, err
	}

	credit := &models.PrivateCredit{
		WalletID:   req.ToWalletID,
		MovementID: req.MovementID,
		Type:       models.MoveTransfer,
		FromWallet: req.FromWalletID,
		Amount:     req.Amount,
		Salt:       req.Salt,
		Timestamp:  tx.Timestamp,
	}
	if err := sim.PutJSON(c.ns.Credits, creditKey(req.ToWalletID, req.MovementID), credit); err != nil {
		return nil, err
	}

	// The payer's audit copy; the payee's copy is written when it consolidates.
	detail := &models.PrivateMovement{
		MovementID: req.MovementID,
		Type:       models.MoveTransfer,
		FromWallet: req.FromWalletID,
		ToWallet:   req.ToWalletID,
		Amount:     req.Amount,
		Salt:       req.Salt,
		Timestamp:  tx.Timestamp,
	}
	if err := sim.PutJSON(c.BalanceNamespace(fromWallet.OwnerMSP), moveDetailKey(req.MovementID), detail); err != nil {
		return nil, err
	}

	return c.putMovement(sim, tx, req.MovementID, models.MoveTransfer, req.FromWalletID, req.ToWalletID, req.Amount, req.Salt)
}

// RedeemCBDC burns CBDC from the caller's wallet.
func (c *CBDCService) RedeemCBDC(sim *simulator.MultiNamespaceSimulator, tx TxContext, req models.RedeemCBDCRequest) (*models.Movement, error) {
	if err := validateAmount(req.Amount, req.Salt); err != nil {
		return nil, err
	}
	if err := c.requireNewMovement(sim, req.MovementID); err != nil {
		return nil, err
	}

	fromWallet, err := c.getOwnedActiveWallet(sim, tx.CallerMSP, req.WalletID)
	if err != nil {
		return nil, err
	}
	if err := c.debit(sim, tx, fromWallet, req.Amount); err != nil {
		return nil, err
	}

	delta := &models.SupplyDelta{MovementID: req.MovementID, Delta: -req.Amount, Timestamp: tx.Timestamp}
	if err := sim.PutJSON(c.ns.Supply, supplyKey(req.MovementID), delta); err != nil {
		return nil, err
	}

	detail := &models.PrivateMovement{
		MovementID: req.MovementID,
		Type:       models.MoveRedeem,
		FromWallet: req.WalletID,
		Amount:     req.Amount,
		Salt:       req.Salt,
		Timestamp:  tx.Timestamp,
	}
	if err := sim.PutJSON(c.BalanceNamespace(fromWallet.OwnerMSP), moveDetailKey(req.MovementID), detail); err != nil {
		return nil, err
	}

	return c.putMovement(sim, tx, req.MovementID, models.MoveRedeem, req.WalletID, "", req.Amount, req.Salt)
}

func (c *CBDCService) putMovement(sim *simulator.MultiNamespaceSimulator, tx TxContext, id, typ, from, to string, amount int64, salt string) (*models.Movement, error) {
	mv := &models.Movement{
		DocType:    "movement",
		MovementID: id,
		Type:       typ,
		FromWallet: from,
		ToWallet:   to,
		AmountHash: amountHash(id, amount, salt),
		Status:     "SETTLED",
		Timestamp:  tx.Timestamp,
		TxID:       tx.TxID,
	}
	if err := sim.PutJSON(c.ns.Public, movementKey(id), mv); err != nil {
		return nil, err
	}
	return mv, nil
}

// ─── Consolidation & Queries ────────────────────────────────────────────────

// ConsolidateBalance folds the wallet's pending credits into its settled balance.
func (c *CBDCService) ConsolidateBalance(sim *simulator.MultiNamespaceSimulator, tx TxContext, walletID string) (int64, error) {
	w, err := c.getOwnedWallet(sim, tx.CallerMSP, walletID)
	if err != nil {
		return 0, err
	}
	bal, folded, err := c.consolidate(sim, tx, w)
	if err != nil {
		return 0, err
	}
	if folded == 0 {
		return 0, fail(ErrInvalidState, "wallet %s has no pending credits", walletID)
	}
	return bal.Balance, nil
}

func (c *CBDCService) CheckBalance(sim *simulator.MultiNamespaceSimulator, callerMSP, walletID string) (*models.BalanceView, error) {
	w, err := c.getOwnedWallet(sim, callerMSP, walletID)
	if err != nil {
		return nil, err
	}
	bal, err := c.balance(sim, w)
	if err != nil {
		return nil, err
	}
	credits, err := c.pendingCredits(sim, w.WalletID)
	if err != nil {
		return nil, err
	}

	var pending int64
	for _, cr := range credits {
		pending += cr.Amount
	}
	return &models.BalanceView{
		WalletID:       walletID,
		Settled:        bal.Balance,
		PendingCredits: pending,
		Available:      bal.Balance + pending,
	}, nil
}

func (c *CBDCService) QueryWallet(sim *simulator.MultiNamespaceSimulator, walletID string) (*models.Wallet, error) {
	return c.getWallet(sim, walletID)
}

func (c *CBDCService) QueryMovement(sim *simulator.MultiNamespaceSimulator, movementID string) (*models.Movement, error) {
	var m models.Movement
	ok, err := sim.GetJSON(c.ns.Public, movementKey(movementID), &m)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, fail(ErrNotFound, "movement %s does not exist", movementID)
	}
	return &m, nil
}

func (c *CBDCService) VerifyMovementAmount(sim *simulator.MultiNamespaceSimulator, movementID string, amount int64, salt string) (bool, error) {
	m, err := c.QueryMovement(sim, movementID)
	if err != nil {
		return false, err
	}
	return m.AmountHash == amountHash(movementID, amount, salt), nil
}

func (c *CBDCService) QueryTotalSupply(sim *simulator.MultiNamespaceSimulator, callerMSP string) (*models.SupplyView, error) {
	if err := c.requireCentralBank(callerMSP); err != nil {
		return nil, err
	}

	deltas, err := sim.ListPrefix(c.ns.Supply, models.PrefixSupply)
	if err != nil {
		return nil, err
	}
	view := &models.SupplyView{}
	for k, raw := range deltas {
		var d models.SupplyDelta
		if err := json.Unmarshal(raw, &d); err != nil {
			return nil, fmt.Errorf("corrupt supply record %s: %w", k, err)
		}
		view.TotalSupply += d.Delta
		view.Entries++
	}
	return view, nil
}

// ─── Internal Helpers ───────────────────────────────────────────────────────

func (c *CBDCService) getWallet(sim *simulator.MultiNamespaceSimulator, id string) (*models.Wallet, error) {
	if id == "" {
		return nil, fail(ErrInvalidArgument, "walletId must not be empty")
	}
	var w models.Wallet
	ok, err := sim.GetJSON(c.ns.Public, walletKey(id), &w)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, fail(ErrNotFound, "wallet %s does not exist", id)
	}
	return &w, nil
}

func (c *CBDCService) getActiveWallet(sim *simulator.MultiNamespaceSimulator, id string) (*models.Wallet, error) {
	w, err := c.getWallet(sim, id)
	if err != nil {
		return nil, err
	}
	if w.Status != models.WalletActive {
		return nil, fail(ErrBlocked, "wallet %s is %s", id, w.Status)
	}
	return w, nil
}

func (c *CBDCService) getOwnedWallet(sim *simulator.MultiNamespaceSimulator, callerMSP, id string) (*models.Wallet, error) {
	w, err := c.getWallet(sim, id)
	if err != nil {
		return nil, err
	}
	if w.OwnerMSP != callerMSP {
		return nil, fail(ErrAccessDenied, "caller MSP %s does not own wallet %s (owner: %s)", callerMSP, id, w.OwnerMSP)
	}
	return w, nil
}

func (c *CBDCService) getOwnedActiveWallet(sim *simulator.MultiNamespaceSimulator, callerMSP, id string) (*models.Wallet, error) {
	w, err := c.getOwnedWallet(sim, callerMSP, id)
	if err != nil {
		return nil, err
	}
	if w.Status != models.WalletActive {
		return nil, fail(ErrBlocked, "wallet %s is %s", id, w.Status)
	}
	return w, nil
}

func (c *CBDCService) requireNewMovement(sim *simulator.MultiNamespaceSimulator, id string) error {
	if id == "" {
		return fail(ErrInvalidArgument, "movementId must not be empty")
	}
	existing, err := sim.Get(c.ns.Public, movementKey(id))
	if err != nil {
		return err
	}
	if len(existing) > 0 {
		return fail(ErrDuplicate, "movement %s already exists", id)
	}
	return nil
}

// balance returns the wallet's settled balance; a missing record is a zero balance.
func (c *CBDCService) balance(sim *simulator.MultiNamespaceSimulator, w *models.Wallet) (*models.PrivateBalance, error) {
	bal := &models.PrivateBalance{WalletID: w.WalletID}
	if _, err := sim.GetJSON(c.BalanceNamespace(w.OwnerMSP), balanceKey(w.WalletID), bal); err != nil {
		return nil, err
	}
	return bal, nil
}

// pendingCredits returns the wallet's unconsolidated credits keyed by state key.
func (c *CBDCService) pendingCredits(sim *simulator.MultiNamespaceSimulator, walletID string) (map[string]models.PrivateCredit, error) {
	rows, err := sim.ListPrefix(c.ns.Credits, creditPrefix(walletID))
	if err != nil {
		return nil, err
	}
	out := make(map[string]models.PrivateCredit, len(rows))
	for k, raw := range rows {
		var cr models.PrivateCredit
		if err := json.Unmarshal(raw, &cr); err != nil {
			return nil, fmt.Errorf("corrupt credit record %s: %w", k, err)
		}
		out[k] = cr
	}
	return out, nil
}

// consolidate folds pending credits into the balance and records the payee's audit copy
// of each movement. It returns the new balance and the amount folded in.
func (c *CBDCService) consolidate(sim *simulator.MultiNamespaceSimulator, tx TxContext, w *models.Wallet) (*models.PrivateBalance, int64, error) {
	bal, err := c.balance(sim, w)
	if err != nil {
		return nil, 0, err
	}
	credits, err := c.pendingCredits(sim, w.WalletID)
	if err != nil {
		return nil, 0, err
	}
	if len(credits) == 0 {
		return bal, 0, nil
	}

	ownerNS := c.BalanceNamespace(w.OwnerMSP)
	var sum int64
	for k, cr := range credits {
		sum += cr.Amount
		if err := sim.Del(c.ns.Credits, k); err != nil {
			return nil, 0, err
		}
		detail := &models.PrivateMovement{
			MovementID: cr.MovementID,
			Type:       cr.Type,
			FromWallet: cr.FromWallet,
			ToWallet:   w.WalletID,
			Amount:     cr.Amount,
			Salt:       cr.Salt,
			Timestamp:  cr.Timestamp,
		}
		if err := sim.PutJSON(ownerNS, moveDetailKey(cr.MovementID), detail); err != nil {
			return nil, 0, err
		}
	}

	bal.Balance += sum
	bal.Salt = tx.TxID
	bal.UpdatedAt = tx.Timestamp
	if err := sim.PutJSON(ownerNS, balanceKey(w.WalletID), bal); err != nil {
		return nil, 0, err
	}
	return bal, sum, nil
}

func (c *CBDCService) debit(sim *simulator.MultiNamespaceSimulator, tx TxContext, w *models.Wallet, amount int64) error {
	bal, _, err := c.consolidate(sim, tx, w)
	if err != nil {
		return err
	}
	if bal.Balance < amount {
		return fail(ErrInsufficient, "wallet %s has insufficient CBDC", w.WalletID)
	}

	bal.Balance -= amount
	bal.Salt = tx.TxID
	bal.UpdatedAt = tx.Timestamp
	return sim.PutJSON(c.BalanceNamespace(w.OwnerMSP), balanceKey(w.WalletID), bal)
}
