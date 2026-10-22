package models

import "strings"

// Participant types.
const (
	ParticipantCentralBank = "central_bank"
	ParticipantBank        = "bank"
)

// Wallet statuses.
const (
	WalletActive = "ACTIVE"
	WalletFrozen = "FROZEN"
)

// Movement types.
const (
	MoveMint     = "MINT"
	MoveTransfer = "TRANSFER"
	MoveRedeem   = "REDEEM"
)

// Key prefixes.
const (
	PrefixWallet     = "WALLET_"
	PrefixMovement   = "MOVE_"
	PrefixBalance    = "BAL_"
	PrefixCredit     = "CREDIT~"
	PrefixMoveDetail = "MOVEDETAIL_"
	PrefixSupply     = "SUPPLY~"
)

// Default namespace names and the Central Bank MSP ID.
const (
	DefaultPublicNamespace  = "cbdc_public"
	DefaultCentralBankPDC   = "cbdc_cb_pdc"
	DefaultCreditsNamespace = "cbdc_credits"
	CentralBankMSP          = "CentralBankMSP"
)

// PDCNameForMSP returns the balance namespace owned by an MSP:
// "Org1MSP" -> "cbdc_org1_pdc". The Central Bank keeps its own balances in its
// supply namespace, which only it may endorse.
func PDCNameForMSP(mspID string) string {
	if mspID == CentralBankMSP {
		return DefaultCentralBankPDC
	}
	clean := strings.ToLower(strings.TrimSuffix(mspID, "MSP"))
	return "cbdc_" + clean + "_pdc"
}

// Wallet is the public record of a wholesale CBDC wallet.
// Namespace: cbdc_public   Key: WALLET_<walletId>
type Wallet struct {
	DocType         string `json:"docType"`
	WalletID        string `json:"walletId"`
	OwnerMSP        string `json:"ownerMsp"`
	ParticipantType string `json:"participantType"` // central_bank | bank
	Status          string `json:"status"`          // ACTIVE | FROZEN
	FreezeReason    string `json:"freezeReason,omitempty"`
	CreatedAt       string `json:"createdAt"`
	UpdatedAt       string `json:"updatedAt"`
}

// Movement is the public, amount-free record of a mint, transfer or redemption.
// AmountHash = SHA-256(movementId | amount | salt)
// Namespace: cbdc_public   Key: MOVE_<movementId>
type Movement struct {
	DocType    string `json:"docType"`
	MovementID string `json:"movementId"`
	Type       string `json:"type"` // MINT | TRANSFER | REDEEM
	FromWallet string `json:"fromWallet,omitempty"`
	ToWallet   string `json:"toWallet,omitempty"`
	AmountHash string `json:"amountHash"`
	Status     string `json:"status"` // SETTLED
	Timestamp  string `json:"timestamp"`
	TxID       string `json:"txId"`
}

// PrivateBalance is a wallet's consolidated balance. A missing record reads as zero.
// Namespace: PDCNameForMSP(owner)   Key: BAL_<walletId>
type PrivateBalance struct {
	WalletID  string `json:"walletId"`
	Balance   int64  `json:"balance"`
	Salt      string `json:"salt"`
	UpdatedAt string `json:"updatedAt"`
}

// PrivateCredit is an incoming credit not yet consolidated into the balance. Credits live
// in a Central Bank endorsed namespace so a payee does not have to sign incoming payments.
// Namespace: cbdc_credits   Key: CREDIT~<walletId>~<movementId>
type PrivateCredit struct {
	WalletID   string `json:"walletId"`
	MovementID string `json:"movementId"`
	Type       string `json:"type"`
	FromWallet string `json:"fromWallet,omitempty"`
	Amount     int64  `json:"amount"`
	Salt       string `json:"salt"`
	Timestamp  string `json:"timestamp"`
}

// PrivateMovement holds the confidential details of a movement for a party's audit trail.
// The payer's copy is written with the movement, the payee's copy on consolidation.
// Namespace: PDCNameForMSP(party)   Key: MOVEDETAIL_<movementId>
type PrivateMovement struct {
	MovementID string `json:"movementId"`
	Type       string `json:"type"`
	FromWallet string `json:"fromWallet,omitempty"`
	ToWallet   string `json:"toWallet,omitempty"`
	Amount     int64  `json:"amount"`
	Salt       string `json:"salt"`
	Timestamp  string `json:"timestamp"`
}

// SupplyDelta records a change in CBDC in circulation (+mint, -redeem).
// Namespace: cbdc_cb_pdc   Key: SUPPLY~<movementId>
type SupplyDelta struct {
	MovementID string `json:"movementId"`
	Delta      int64  `json:"delta"`
	Timestamp  string `json:"timestamp"`
}

// BalanceView is returned by CheckBalance.
type BalanceView struct {
	WalletID       string `json:"walletId"`
	Settled        int64  `json:"settled"`        // consolidated balance
	PendingCredits int64  `json:"pendingCredits"` // credits not yet consolidated
	Available      int64  `json:"available"`      // settled + pendingCredits
}

// SupplyView is returned by QueryTotalSupply.
type SupplyView struct {
	TotalSupply int64 `json:"totalSupply"`
	Entries     int   `json:"entries"`
}

// --- REST API Request & Response Structures ---

type CreateWalletRequest struct {
	WalletID        string `json:"walletId"`
	OwnerMSP        string `json:"ownerMsp"`
	ParticipantType string `json:"participantType"` // "bank" or "central_bank"
}

// WalletStatusRequest freezes or unfreezes a wallet. WalletID comes from the URL path.
type WalletStatusRequest struct {
	WalletID string `json:"walletId"`
	Reason   string `json:"reason,omitempty"`
}

// ConsolidateRequest folds pending credits into a balance. WalletID comes from the URL path.
type ConsolidateRequest struct {
	WalletID string `json:"walletId"`
}

type MintCBDCRequest struct {
	WalletID   string `json:"walletId"`
	MovementID string `json:"movementId"`
	Amount     int64  `json:"amount"`
	Salt       string `json:"salt"`
}

type TransferCBDCRequest struct {
	FromWalletID string `json:"fromWalletId"`
	ToWalletID   string `json:"toWalletId"`
	MovementID   string `json:"movementId"`
	Amount       int64  `json:"amount"`
	Salt         string `json:"salt"`
}

type RedeemCBDCRequest struct {
	WalletID   string `json:"walletId"`
	MovementID string `json:"movementId"`
	Amount     int64  `json:"amount"`
	Salt       string `json:"salt"`
}

type VerifyMovementRequest struct {
	Amount int64  `json:"amount"`
	Salt   string `json:"salt"`
}

// ReadRecord is a read of a key at a version. A nil version means "key must not exist".
type ReadRecord struct {
	Key     string  `json:"key"`
	Version *uint64 `json:"version"`
}

// WriteRecord is a write of a key. An empty value deletes the key.
type WriteRecord struct {
	Key     string  `json:"key"`
	Version *uint64 `json:"version,omitempty"` // read-writes: the version read before the update
	Value   string  `json:"value"`
}

// NamespaceReadWriteSet is a JSON view of one Fabric-X TxNamespace.
type NamespaceReadWriteSet struct {
	Namespace   string        `json:"namespace"`
	NsVersion   uint64        `json:"nsVersion"`
	ReadsOnly   []ReadRecord  `json:"readsOnly,omitempty"`
	ReadWrites  []WriteRecord `json:"readWrites,omitempty"`
	BlindWrites []WriteRecord `json:"blindWrites,omitempty"`
}

// EndorsementInfo describes one endorsement attached to a namespace.
type EndorsementInfo struct {
	Namespace   string `json:"namespace"`
	EndorserMSP string `json:"endorserMsp"`
	Signature   string `json:"signature"` // hex-encoded DER ECDSA signature
}

// Transaction outcomes reported by mutation endpoints.
const (
	TxStatusCommitted = "COMMITTED" // committed to the ledger
	TxStatusEndorsed  = "ENDORSED"  // fully endorsed, not submitted (?submit=false)
	TxStatusSubmitted = "SUBMITTED" // broadcast, commit status not known before the timeout
	TxStatusQueried   = "QUERIED"   // read-only query
)

// EndorsementResponse is returned by every endpoint. Mutations carry the endorsed
// read/write set and, depending on how far the transaction got, its ledger status
// or the signed envelope.
type EndorsementResponse struct {
	TxID         string                           `json:"txId,omitempty"`
	Status       string                           `json:"status"`
	Payload      any                              `json:"payload,omitempty"`
	ReadWriteSet map[string]NamespaceReadWriteSet `json:"readWriteSet,omitempty"`
	Endorsements []EndorsementInfo                `json:"endorsements,omitempty"`
	Envelope     string                           `json:"envelope,omitempty"` // base64 common.Envelope
}

// ErrorResponse represents an error return from the REST API.
type ErrorResponse struct {
	Error string `json:"error"`
	Code  string `json:"code,omitempty"`
	TxID  string `json:"txId,omitempty"`
}
