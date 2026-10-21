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

// Default Namespace Names.
const (
	DefaultPublicNamespace = "cbdc_public"
	DefaultCentralBankPDC  = "cbdc_cb_pdc"
	CentralBankMSP         = "CentralBankMSP"
)


// PDCNameForMSP returns the private namespace for a given MSP ID.
func PDCNameForMSP(mspID string) string {
	// Strip "MSP" suffix and make lowercase to conform to Fabric-X namespace naming
	clean := strings.ToLower(strings.TrimSuffix(mspID, "MSP"))
	return "cbdc_" + clean + "_pdc"
}

// Wallet is the public record of a wholesale CBDC wallet.
// Key: WALLET_<walletId>
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
// Key: MOVE_<movementId>
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

// PrivateBalance is a wallet's consolidated balance.
// Namespace: <OwnerMSP>_pdc   Key: BAL_<walletId>
type PrivateBalance struct {
	WalletID  string `json:"walletId"`
	Balance   int64  `json:"balance"`
	Salt      string `json:"salt"`
	UpdatedAt string `json:"updatedAt"`
}

// PrivateCredit is an incoming credit not yet consolidated into the balance.
// Namespace: <OwnerMSP>_pdc   Key: CREDIT~<walletId>~<movementId>
type PrivateCredit struct {
	WalletID   string `json:"walletId"`
	MovementID string `json:"movementId"`
	FromWallet string `json:"fromWallet,omitempty"`
	Amount     int64  `json:"amount"`
	Salt       string `json:"salt"`
}

// PrivateMovement holds the confidential details of a movement for party audit trails.
// Namespace: <MSP>_pdc   Key: MOVEDETAIL_<movementId>
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
// Namespace: CentralBankPDC   Key: SUPPLY~<movementId>
type SupplyDelta struct {
	MovementID string `json:"movementId"`
	Delta      int64  `json:"delta"`
	Timestamp  string `json:"timestamp"`
}

// AmountInput is the confidential payload for MintCBDC, TransferCBDC, RedeemCBDC.
type AmountInput struct {
	Amount int64  `json:"amount"`
	Salt   string `json:"salt"` // >= 16 random characters
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

type FreezeWalletRequest struct {
	Reason string `json:"reason"`
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
	MovementID string `json:"movementId"`
	Amount     int64  `json:"amount"`
	Salt       string `json:"salt"`
}

// ReadRecord represents a read operation on a key with its version.
type ReadRecord struct {
	Key     string  `json:"key"`
	Version *uint64 `json:"version"`
}

// WriteRecord represents a write operation on a key.
type WriteRecord struct {
	Key     string  `json:"key"`
	Version *uint64 `json:"version,omitempty"` // For ReadWrites: the version read before update
	Value   string  `json:"value"`             // JSON or string payload
}

// NamespaceReadWriteSet represents the Fabric-X Read/Write Set for one namespace.
type NamespaceReadWriteSet struct {
	Namespace   string        `json:"namespace"`
	NsVersion   uint64        `json:"nsVersion"`
	ReadsOnly   []ReadRecord  `json:"readsOnly,omitempty"`
	ReadWrites  []WriteRecord `json:"readWrites,omitempty"`
	BlindWrites []WriteRecord `json:"blindWrites,omitempty"`
}

// EndorsementSignature represents the endorser's cryptographic signature.
type EndorsementSignature struct {
	EndorserMSP string `json:"endorserMsp"`
	Identity    string `json:"identity"`    // base64 / PEM / ID string
	Signature   string `json:"signature"`   // hex-encoded ECDSA signature
	Timestamp   string `json:"timestamp"`
}

// EndorsementResponse is the standard REST response containing the generated Read/Write set.
type EndorsementResponse struct {
	TxID         string                           `json:"txId"`
	Status       string                           `json:"status"` // "ENDORSED" or "QUERIED"
	Payload      any                              `json:"payload,omitempty"`
	ReadWriteSet map[string]NamespaceReadWriteSet `json:"readWriteSet,omitempty"`
	Endorsement  *EndorsementSignature            `json:"endorsement,omitempty"`
}

// ErrorResponse represents an error return from the REST API.
type ErrorResponse struct {
	Error   string `json:"error"`
	Code    string `json:"code,omitempty"`
	Details string `json:"details,omitempty"`
}
