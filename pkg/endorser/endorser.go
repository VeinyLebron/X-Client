package endorser

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/asn1"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"math/big"
	"os"
	"sync"
	"time"

	"x-client/pkg/models"
)

type ecdsaSignature struct {
	R, S *big.Int
}

// Config holds endorser crypto configuration.
type Config struct {
	MSPID    string `yaml:"msp_id"`
	CertPath string `yaml:"cert_path"`
	KeyPath  string `yaml:"key_path"`
}

// Endorser manages endorsement signing for a single organization.
type Endorser struct {
	mspID      string
	certPEM    []byte
	privateKey *ecdsa.PrivateKey
}

// New creates an Endorser. If cert/key paths are not provided or don't exist,
// it generates an in-memory ECDSA P-256 keypair for simulation.
func New(cfg Config) (*Endorser, error) {
	mspID := cfg.MSPID
	if mspID == "" {
		mspID = "Org1MSP"
	}

	var privKey *ecdsa.PrivateKey
	var certPEM []byte

	if cfg.KeyPath != "" && cfg.CertPath != "" {
		if kData, err := os.ReadFile(cfg.KeyPath); err == nil {
			block, _ := pem.Decode(kData)
			if block != nil {
				if parsedKey, err := x509.ParsePKCS8PrivateKey(block.Bytes); err == nil {
					if ecKey, ok := parsedKey.(*ecdsa.PrivateKey); ok {
						privKey = ecKey
					}
				} else if ecKey, err := x509.ParseECPrivateKey(block.Bytes); err == nil {
					privKey = ecKey
				}
			}
		}
		if cData, err := os.ReadFile(cfg.CertPath); err == nil {
			certPEM = cData
		}
	}

	if privKey == nil {
		var err error
		privKey, err = ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		if err != nil {
			return nil, fmt.Errorf("generate fallback key: %w", err)
		}
		certPEM = []byte(fmt.Sprintf("-----BEGIN CERTIFICATE-----\n[SIMULATED CERTIFICATE FOR %s]\n-----END CERTIFICATE-----\n", mspID))
	}

	return &Endorser{
		mspID:      mspID,
		certPEM:    certPEM,
		privateKey: privKey,
	}, nil
}

func (e *Endorser) MSPID() string {
	return e.mspID
}

// SignReadWriteSet signs the serialized Read/Write sets using SHA-256 and ECDSA.
func (e *Endorser) SignReadWriteSet(rwSets map[string]models.NamespaceReadWriteSet) (*models.EndorsementSignature, error) {
	payload, err := json.Marshal(rwSets)
	if err != nil {
		return nil, fmt.Errorf("marshal rwsets: %w", err)
	}

	digest := sha256.Sum256(payload)

	r, s, err := ecdsa.Sign(rand.Reader, e.privateKey, digest[:])
	if err != nil {
		return nil, fmt.Errorf("ecdsa sign: %w", err)
	}

	// Low-S standard (BIP 62 / Fabric requirement)
	curveOrder := e.privateKey.Curve.Params().N
	halfOrder := new(big.Int).Rsh(curveOrder, 1)
	if s.Cmp(halfOrder) > 0 {
		s.Sub(curveOrder, s)
	}

	sigBytes, err := asn1.Marshal(ecdsaSignature{R: r, S: s})
	if err != nil {
		return nil, fmt.Errorf("asn1 marshal signature: %w", err)
	}

	return &models.EndorsementSignature{
		EndorserMSP: e.mspID,
		Identity:    string(e.certPEM),
		Signature:   hex.EncodeToString(sigBytes),
		Timestamp:   time.Now().UTC().Format(time.RFC3339),
	}, nil
}

// --- Registry: Manages multiple Endorser identities (e.g. 3 orgs) ---

type Registry struct {
	mu           sync.RWMutex
	defaultMSPID string
	endorsers    map[string]*Endorser
}

func NewRegistry(defaultMSPID string) *Registry {
	if defaultMSPID == "" {
		defaultMSPID = models.CentralBankMSP
	}
	return &Registry{
		defaultMSPID: defaultMSPID,
		endorsers:    make(map[string]*Endorser),
	}
}

func (r *Registry) DefaultMSPID() string {
	return r.defaultMSPID
}

func (r *Registry) Register(e *Endorser) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.endorsers[e.MSPID()] = e
}

func (r *Registry) Get(mspID string) *Endorser {
	r.mu.RLock()
	if mspID == "" {
		mspID = r.defaultMSPID
	}
	if e, ok := r.endorsers[mspID]; ok {
		r.mu.RUnlock()
		return e
	}
	r.mu.RUnlock()

	// Lazily create an ephemeral signer for this MSP ID if not explicitly registered
	r.mu.Lock()
	defer r.mu.Unlock()
	if e, ok := r.endorsers[mspID]; ok {
		return e
	}
	e, _ := New(Config{MSPID: mspID})
	r.endorsers[mspID] = e
	return e
}
