package contract

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"x-client/pkg/models"
	"x-client/pkg/simulator"
)

// Standard error codes matching the chaincode.
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

func fail(code, format string, args ...any) error {
	return fmt.Errorf("%s: %s", code, fmt.Sprintf(format, args...))
}

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
func creditKey(toWallet, movementID string) string {
	return models.PrefixCredit + toWallet + "~" + movementID
}
func moveDetailKey(id string) string { return models.PrefixMoveDetail + id }
func supplyKey(id string) string     { return models.PrefixSupply + id }

// CBDCService implements wholesale CBDC smart contract rules.
type CBDCService struct {
	PublicNS string
	CBNamespace string
}

func NewCBDCService() *CBDCService {
	return &CBDCService{
		PublicNS:    models.DefaultPublicNamespace,
		CBNamespace: models.DefaultCentralBankPDC,
	}
}

// ─── Wallets ─────────────────────────────────────────────────────────────────

func (c *CBDCService) CreateWallet(sim *simulator.MultiNamespaceSimulator, callerMSP, txID string, req models.CreateWalletRequest) (*models.Wallet, error) {
	if callerMSP != models.CentralBankMSP {
		return nil, fail(ErrAccessDenied, "caller MSP %s is not authorised (allowed: %s)", callerMSP, models.CentralBankMSP)
	}
	if req.WalletID == "" || req.OwnerMSP == "" {
		return nil, fail(ErrInvalidArgument, "walletID and ownerMSP must not be empty")
	}
	if req.ParticipantType != models.ParticipantBank && req.ParticipantType != models.ParticipantCentralBank {
		return nil, fail(ErrInvalidArgument, "participantType must be one of bank, central_bank")
	}
	if (req.ParticipantType == models.ParticipantCentralBank) != (req.OwnerMSP == models.CentralBankMSP) {
		return nil, fail(ErrInvalidArgument, "participantType central_bank is reserved for %s", models.CentralBankMSP)
	}

	// Check if already exists
	existing, err := sim.Get(c.PublicNS, walletKey(req.WalletID))
	if err != nil {
		return nil, err
	}
	if len(existing) > 0 {
		return nil, fail(ErrAlreadyExists, "wallet %s already exists", req.WalletID)
	}

	now := time.Now().UTC().Format(time.RFC3339)
	w := &models.Wallet{
		DocType:         "wholesaleWallet",
		WalletID:        req.WalletID,
		OwnerMSP:        req.OwnerMSP,
		ParticipantType: req.ParticipantType,
		Status:          models.WalletActive,
		CreatedAt:       now,
		UpdatedAt:       now,
	}

	// Write public wallet
	if err := sim.PutJSON(c.PublicNS, walletKey(req.WalletID), w); err != nil {
		return nil, err
	}

	// Initialize balance in owner PDC namespace
	ownerPDC := models.PDCNameForMSP(req.OwnerMSP)
	bal := &models.PrivateBalance{
		WalletID:  req.WalletID,
		Balance:   0,
		Salt:      txID,
		UpdatedAt: now,
	}
	if err := sim.PutJSON(ownerPDC, balanceKey(req.WalletID), bal); err != nil {
		return nil, err
	}

	return w, nil
}

func (c *CBDCService) FreezeWallet(sim *simulator.MultiNamespaceSimulator, callerMSP string, walletID, reason string) (*models.Wallet, error) {
	if callerMSP != models.CentralBankMSP {
		return nil, fail(ErrAccessDenied, "caller MSP %s is not authorised (allowed: %s)", callerMSP, models.CentralBankMSP)
	}
	if reason == "" {
		return nil, fail(ErrInvalidArgument, "reason must not be empty")
	}
	return c.setWalletStatus(sim, walletID, models.WalletFrozen, reason)
}

func (c *CBDCService) UnfreezeWallet(sim *simulator.MultiNamespaceSimulator, callerMSP string, walletID string) (*models.Wallet, error) {
	if callerMSP != models.CentralBankMSP {
		return nil, fail(ErrAccessDenied, "caller MSP %s is not authorised (allowed: %s)", callerMSP, models.CentralBankMSP)
	}
	return c.setWalletStatus(sim, walletID, models.WalletActive, "")
}

func (c *CBDCService) setWalletStatus(sim *simulator.MultiNamespaceSimulator, walletID, status, reason string) (*models.Wallet, error) {
	var w models.Wallet
	ok, err := sim.GetJSON(c.PublicNS, walletKey(walletID), &w)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, fail(ErrNotFound, "wallet %s does not exist", walletID)
	}
	if w.Status == status {
		return nil, fail(ErrInvalidState, "wallet %s is already %s", walletID, status)
	}

	w.Status = status
	w.FreezeReason = reason
	w.UpdatedAt = time.Now().UTC().Format(time.RFC3339)

	if err := sim.PutJSON(c.PublicNS, walletKey(walletID), &w); err != nil {
		return nil, err
	}
	return &w, nil
}

// ─── Movements ───────────────────────────────────────────────────────────────

func (c *CBDCService) MintCBDC(sim *simulator.MultiNamespaceSimulator, callerMSP, txID string, req models.MintCBDCRequest) (*models.Movement, error) {
	if callerMSP != models.CentralBankMSP {
		return nil, fail(ErrAccessDenied, "caller MSP %s is not authorised (allowed: %s)", callerMSP, models.CentralBankMSP)
	}
	if req.Amount <= 0 {
		return nil, fail(ErrInvalidArgument, "amount must be > 0 (got %d)", req.Amount)
	}
	if len(req.Salt) < 16 {
		return nil, fail(ErrInvalidArgument, "salt must be at least 16 characters")
	}
	if err := c.requireNewMovement(sim, req.MovementID); err != nil {
		return nil, err
	}

	toWallet, err := c.getActiveWallet(sim, req.WalletID)
	if err != nil {
		return nil, err
	}

	now := time.Now().UTC().Format(time.RFC3339)

	// Blind credit to payee's PDC
	toPDC := models.PDCNameForMSP(toWallet.OwnerMSP)
	credit := &models.PrivateCredit{
		WalletID:   req.WalletID,
		MovementID: req.MovementID,
		Amount:     req.Amount,
		Salt:       req.Salt,
	}
	if err := sim.PutJSON(toPDC, creditKey(req.WalletID, req.MovementID), credit); err != nil {
		return nil, err
	}

	// Supply delta to CentralBank PDC
	delta := &models.SupplyDelta{
		MovementID: req.MovementID,
		Delta:      req.Amount,
		Timestamp:  now,
	}
	if err := sim.PutJSON(c.CBNamespace, supplyKey(req.MovementID), delta); err != nil {
		return nil, err
	}

	// Public movement record
	mv := &models.Movement{
		DocType:    "movement",
		MovementID: req.MovementID,
		Type:       models.MoveMint,
		FromWallet: "",
		ToWallet:   req.WalletID,
		AmountHash: amountHash(req.MovementID, req.Amount, req.Salt),
		Status:     "SETTLED",
		Timestamp:  now,
		TxID:       txID,
	}
	if err := sim.PutJSON(c.PublicNS, movementKey(req.MovementID), mv); err != nil {
		return nil, err
	}

	// Private detail to payee PDC
	detail := &models.PrivateMovement{
		MovementID: req.MovementID,
		Type:       models.MoveMint,
		FromWallet: "",
		ToWallet:   req.WalletID,
		Amount:     req.Amount,
		Salt:       req.Salt,
		Timestamp:  now,
	}
	if err := sim.PutJSON(toPDC, moveDetailKey(req.MovementID), detail); err != nil {
		return nil, err
	}

	return mv, nil
}

func (c *CBDCService) TransferCBDC(sim *simulator.MultiNamespaceSimulator, callerMSP, txID string, req models.TransferCBDCRequest) (*models.Movement, error) {
	if req.FromWalletID == req.ToWalletID {
		return nil, fail(ErrInvalidArgument, "source and destination wallets must differ")
	}
	if req.Amount <= 0 {
		return nil, fail(ErrInvalidArgument, "amount must be > 0 (got %d)", req.Amount)
	}
	if len(req.Salt) < 16 {
		return nil, fail(ErrInvalidArgument, "salt must be at least 16 characters")
	}
	if err := c.requireNewMovement(sim, req.MovementID); err != nil {
		return nil, err
	}

	fromWallet, err := c.getOwnedActiveWallet(sim, callerMSP, req.FromWalletID)
	if err != nil {
		return nil, err
	}
	toWallet, err := c.getActiveWallet(sim, req.ToWalletID)
	if err != nil {
		return nil, err
	}

	// Debit fromWallet (consolidates pending credits first)
	if err := c.debit(sim, fromWallet, req.Amount, txID); err != nil {
		return nil, err
	}

	// Blind credit to toWallet in toPDC
	toPDC := models.PDCNameForMSP(toWallet.OwnerMSP)
	credit := &models.PrivateCredit{
		WalletID:   req.ToWalletID,
		MovementID: req.MovementID,
		FromWallet: req.FromWalletID,
		Amount:     req.Amount,
		Salt:       req.Salt,
	}
	if err := sim.PutJSON(toPDC, creditKey(req.ToWalletID, req.MovementID), credit); err != nil {
		return nil, err
	}

	now := time.Now().UTC().Format(time.RFC3339)

	// Public movement
	mv := &models.Movement{
		DocType:    "movement",
		MovementID: req.MovementID,
		Type:       models.MoveTransfer,
		FromWallet: req.FromWalletID,
		ToWallet:   req.ToWalletID,
		AmountHash: amountHash(req.MovementID, req.Amount, req.Salt),
		Status:     "SETTLED",
		Timestamp:  now,
		TxID:       txID,
	}
	if err := sim.PutJSON(c.PublicNS, movementKey(req.MovementID), mv); err != nil {
		return nil, err
	}

	// Private detail to both party PDCs
	detail := &models.PrivateMovement{
		MovementID: req.MovementID,
		Type:       models.MoveTransfer,
		FromWallet: req.FromWalletID,
		ToWallet:   req.ToWalletID,
		Amount:     req.Amount,
		Salt:       req.Salt,
		Timestamp:  now,
	}

	fromPDC := models.PDCNameForMSP(fromWallet.OwnerMSP)
	if err := sim.PutJSON(fromPDC, moveDetailKey(req.MovementID), detail); err != nil {
		return nil, err
	}
	if fromPDC != toPDC {
		if err := sim.PutJSON(toPDC, moveDetailKey(req.MovementID), detail); err != nil {
			return nil, err
		}
	}

	return mv, nil
}

func (c *CBDCService) RedeemCBDC(sim *simulator.MultiNamespaceSimulator, callerMSP, txID string, req models.RedeemCBDCRequest) (*models.Movement, error) {
	if req.Amount <= 0 {
		return nil, fail(ErrInvalidArgument, "amount must be > 0 (got %d)", req.Amount)
	}
	if len(req.Salt) < 16 {
		return nil, fail(ErrInvalidArgument, "salt must be at least 16 characters")
	}
	if err := c.requireNewMovement(sim, req.MovementID); err != nil {
		return nil, err
	}

	fromWallet, err := c.getOwnedActiveWallet(sim, callerMSP, req.WalletID)
	if err != nil {
		return nil, err
	}

	// Debit wallet
	if err := c.debit(sim, fromWallet, req.Amount, txID); err != nil {
		return nil, err
	}

	now := time.Now().UTC().Format(time.RFC3339)

	// Negative supply delta to CentralBank PDC
	delta := &models.SupplyDelta{
		MovementID: req.MovementID,
		Delta:      -req.Amount,
		Timestamp:  now,
	}
	if err := sim.PutJSON(c.CBNamespace, supplyKey(req.MovementID), delta); err != nil {
		return nil, err
	}

	// Public movement
	mv := &models.Movement{
		DocType:    "movement",
		MovementID: req.MovementID,
		Type:       models.MoveRedeem,
		FromWallet: req.WalletID,
		ToWallet:   "",
		AmountHash: amountHash(req.MovementID, req.Amount, req.Salt),
		Status:     "SETTLED",
		Timestamp:  now,
		TxID:       txID,
	}
	if err := sim.PutJSON(c.PublicNS, movementKey(req.MovementID), mv); err != nil {
		return nil, err
	}

	// Private detail to owner PDC
	detail := &models.PrivateMovement{
		MovementID: req.MovementID,
		Type:       models.MoveRedeem,
		FromWallet: req.WalletID,
		ToWallet:   "",
		Amount:     req.Amount,
		Salt:       req.Salt,
		Timestamp:  now,
	}
	ownerPDC := models.PDCNameForMSP(fromWallet.OwnerMSP)
	if err := sim.PutJSON(ownerPDC, moveDetailKey(req.MovementID), detail); err != nil {
		return nil, err
	}

	return mv, nil
}

// ─── Consolidation & Balance Queries ────────────────────────────────────────

func (c *CBDCService) ConsolidateBalance(sim *simulator.MultiNamespaceSimulator, callerMSP, txID, walletID string) (int64, error) {
	w, err := c.getOwnedWallet(sim, callerMSP, walletID)
	if err != nil {
		return 0, err
	}
	bal, _, err := c.consolidate(sim, w, txID)
	if err != nil {
		return 0, err
	}
	return bal.Balance, nil
}

func (c *CBDCService) CheckBalance(sim *simulator.MultiNamespaceSimulator, callerMSP, walletID string) (*models.BalanceView, error) {
	w, err := c.getOwnedWallet(sim, callerMSP, walletID)
	if err != nil {
		return nil, err
	}

	ownerPDC := models.PDCNameForMSP(w.OwnerMSP)
	var bal models.PrivateBalance
	ok, err := sim.GetJSON(ownerPDC, balanceKey(walletID), &bal)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, fail(ErrNotFound, "private balance of %s not found", walletID)
	}

	credits, err := c.pendingCredits(sim, w)
	if err != nil {
		return nil, err
	}

	return &models.BalanceView{
		WalletID:       walletID,
		Settled:        bal.Balance,
		PendingCredits: credits,
		Available:      bal.Balance + credits,
	}, nil
}

func (c *CBDCService) QueryWallet(sim *simulator.MultiNamespaceSimulator, walletID string) (*models.Wallet, error) {
	return c.getWallet(sim, walletID)
}

func (c *CBDCService) QueryMovement(sim *simulator.MultiNamespaceSimulator, movementID string) (*models.Movement, error) {
	var m models.Movement
	ok, err := sim.GetJSON(c.PublicNS, movementKey(movementID), &m)
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
	if callerMSP != models.CentralBankMSP {
		return nil, fail(ErrAccessDenied, "caller MSP %s is not authorised (allowed: %s)", callerMSP, models.CentralBankMSP)
	}

	deltas, err := sim.ListPrefix(c.CBNamespace, models.PrefixSupply)
	if err != nil {
		return nil, err
	}

	view := &models.SupplyView{}
	for _, raw := range deltas {
		var d models.SupplyDelta
		if err := json.Unmarshal(raw, &d); err == nil {
			view.TotalSupply += d.Delta
			view.Entries++
		}
	}
	return view, nil
}

// ─── Internal Helpers ───────────────────────────────────────────────────────

func (c *CBDCService) getWallet(sim *simulator.MultiNamespaceSimulator, id string) (*models.Wallet, error) {
	var w models.Wallet
	ok, err := sim.GetJSON(c.PublicNS, walletKey(id), &w)
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
		return fail(ErrInvalidArgument, "movementID must not be empty")
	}
	existing, err := sim.Get(c.PublicNS, movementKey(id))
	if err != nil {
		return err
	}
	if len(existing) > 0 {
		return fail(ErrDuplicate, "movement %s already exists", id)
	}
	return nil
}

func (c *CBDCService) pendingCredits(sim *simulator.MultiNamespaceSimulator, w *models.Wallet) (int64, error) {
	ownerPDC := models.PDCNameForMSP(w.OwnerMSP)
	prefix := models.PrefixCredit + w.WalletID + "~"
	rows, err := sim.ListPrefix(ownerPDC, prefix)
	if err != nil {
		return 0, err
	}
	var sum int64
	for _, raw := range rows {
		var cr models.PrivateCredit
		if err := json.Unmarshal(raw, &cr); err == nil {
			sum += cr.Amount
		}
	}
	return sum, nil
}

func (c *CBDCService) consolidate(sim *simulator.MultiNamespaceSimulator, w *models.Wallet, txID string) (*models.PrivateBalance, int64, error) {
	ownerPDC := models.PDCNameForMSP(w.OwnerMSP)
	var bal models.PrivateBalance
	ok, err := sim.GetJSON(ownerPDC, balanceKey(w.WalletID), &bal)
	if err != nil {
		return nil, 0, err
	}
	if !ok {
		return nil, 0, fail(ErrNotFound, "private balance of %s not found", w.WalletID)
	}

	prefix := models.PrefixCredit + w.WalletID + "~"
	rows, err := sim.ListPrefix(ownerPDC, prefix)
	if err != nil {
		return nil, 0, err
	}

	if len(rows) == 0 {
		return &bal, 0, nil
	}

	var sum int64
	for k, raw := range rows {
		var cr models.PrivateCredit
		if err := json.Unmarshal(raw, &cr); err == nil {
			sum += cr.Amount
		}
		if err := sim.Del(ownerPDC, k); err != nil {
			return nil, 0, err
		}
	}

	bal.Balance += sum
	bal.Salt = txID
	bal.UpdatedAt = time.Now().UTC().Format(time.RFC3339)

	if err := sim.PutJSON(ownerPDC, balanceKey(w.WalletID), &bal); err != nil {
		return nil, 0, err
	}

	return &bal, sum, nil
}

func (c *CBDCService) debit(sim *simulator.MultiNamespaceSimulator, w *models.Wallet, amount int64, txID string) error {
	bal, _, err := c.consolidate(sim, w, txID)
	if err != nil {
		return err
	}
	if bal.Balance < amount {
		return fail(ErrInsufficient, "wallet %s has insufficient CBDC", w.WalletID)
	}

	bal.Balance -= amount
	bal.Salt = txID
	bal.UpdatedAt = time.Now().UTC().Format(time.RFC3339)

	ownerPDC := models.PDCNameForMSP(w.OwnerMSP)
	return sim.PutJSON(ownerPDC, balanceKey(w.WalletID), bal)
}
