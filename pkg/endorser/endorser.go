// Package endorser holds an organization's signing identity and produces Fabric-X
// namespace endorsements.
package endorser

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"os"
	"time"

	"github.com/hyperledger/fabric-lib-go/bccsp/utils"
	"github.com/hyperledger/fabric-protos-go-apiv2/msp"
	"github.com/hyperledger/fabric-x-common/api/applicationpb"
	"github.com/hyperledger/fabric-x-common/api/msppb"
	"google.golang.org/protobuf/proto"
)

// Config locates an organization's signing certificate and private key.
type Config struct {
	MSPID    string `yaml:"msp_id"`
	CertPath string `yaml:"cert_path"`
	KeyPath  string `yaml:"key_path"`
}

// Identity is an ECDSA signing identity of one MSP. It implements the fabric-x-sdk Signer.
type Identity struct {
	mspID   string
	certPEM []byte
	cert    *x509.Certificate
	key     *ecdsa.PrivateKey
}

// Load reads the identity's certificate and key. Unlike the previous behaviour it never
// falls back to a random key: a misconfigured identity is a startup error.
func Load(cfg Config) (*Identity, error) {
	if cfg.MSPID == "" {
		return nil, errors.New("msp_id is required")
	}
	certPEM, err := os.ReadFile(cfg.CertPath)
	if err != nil {
		return nil, fmt.Errorf("%s: read certificate: %w", cfg.MSPID, err)
	}
	cert, err := parseCert(certPEM)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", cfg.MSPID, err)
	}
	keyPEM, err := os.ReadFile(cfg.KeyPath)
	if err != nil {
		return nil, fmt.Errorf("%s: read private key: %w", cfg.MSPID, err)
	}
	key, err := parseKey(keyPEM)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", cfg.MSPID, err)
	}
	pub, ok := cert.PublicKey.(*ecdsa.PublicKey)
	if !ok || !pub.Equal(&key.PublicKey) {
		return nil, fmt.Errorf("%s: private key does not match certificate %s", cfg.MSPID, cfg.CertPath)
	}
	return &Identity{mspID: cfg.MSPID, certPEM: certPEM, cert: cert, key: key}, nil
}

// Generate creates a throwaway self-signed identity. Only for in-memory mode and tests:
// the network will not accept its signatures.
func Generate(mspID string) (*Identity, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, err
	}
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(time.Now().UnixNano()),
		Subject:               pkix.Name{CommonName: "ephemeral." + mspID, Organization: []string{mspID}},
		NotBefore:             time.Now().Add(-time.Minute),
		NotAfter:              time.Now().Add(365 * 24 * time.Hour),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		IsCA:                  true,
		BasicConstraintsValid: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		return nil, err
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		return nil, err
	}
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	return &Identity{mspID: mspID, certPEM: certPEM, cert: cert, key: key}, nil
}

func parseCert(certPEM []byte) (*x509.Certificate, error) {
	block, _ := pem.Decode(certPEM)
	if block == nil {
		return nil, errors.New("certificate is not PEM encoded")
	}
	return x509.ParseCertificate(block.Bytes)
}

func parseKey(keyPEM []byte) (*ecdsa.PrivateKey, error) {
	block, _ := pem.Decode(keyPEM)
	if block == nil {
		return nil, errors.New("private key is not PEM encoded")
	}
	if k, err := x509.ParsePKCS8PrivateKey(block.Bytes); err == nil {
		ec, ok := k.(*ecdsa.PrivateKey)
		if !ok {
			return nil, errors.New("private key is not ECDSA")
		}
		return ec, nil
	}
	return x509.ParseECPrivateKey(block.Bytes)
}

func (i *Identity) MSPID() string                  { return i.mspID }
func (i *Identity) CertPEM() []byte                { return i.certPEM }
func (i *Identity) Certificate() *x509.Certificate { return i.cert }

// Sign hashes msg with SHA-256 and returns a low-S DER-encoded ECDSA signature, as
// Fabric MSP verification expects.
func (i *Identity) Sign(msg []byte) ([]byte, error) {
	digest := sha256.Sum256(msg)
	r, s, err := ecdsa.Sign(rand.Reader, i.key, digest[:])
	if err != nil {
		return nil, err
	}
	if s, err = utils.ToLowS(&i.key.PublicKey, s); err != nil {
		return nil, err
	}
	return utils.MarshalECDSASignature(r, s)
}

// Serialize returns the identity as a Fabric SerializedIdentity, used as the envelope creator.
func (i *Identity) Serialize() ([]byte, error) {
	return proto.Marshal(&msp.SerializedIdentity{Mspid: i.mspID, IdBytes: i.certPEM})
}

// EndorseNamespace signs one namespace of transaction txID. Fabric-X endorsements cover
// the ASN.1 encoding of the namespace together with the transaction ID and metadata.
func (i *Identity) EndorseNamespace(ns *applicationpb.TxNamespace, txID string, metadata [][]byte) (*applicationpb.EndorsementWithIdentity, error) {
	msg, err := ns.ASN1Marshal(txID, metadata)
	if err != nil {
		return nil, err
	}
	sig, err := i.Sign(msg)
	if err != nil {
		return nil, fmt.Errorf("sign namespace %s: %w", ns.NsId, err)
	}
	return &applicationpb.EndorsementWithIdentity{
		Endorsement: sig,
		Identity:    msppb.NewIdentity(i.mspID, i.certPEM),
	}, nil
}

// VerifyEndorsement checks that e is a valid signature over namespace ns of txID by the
// certificate it carries. It does not check that the certificate belongs to its MSP.
func VerifyEndorsement(ns *applicationpb.TxNamespace, txID string, metadata [][]byte, e *applicationpb.EndorsementWithIdentity) error {
	cert, err := parseCert(e.GetIdentity().GetCertificate())
	if err != nil {
		return err
	}
	pub, ok := cert.PublicKey.(*ecdsa.PublicKey)
	if !ok {
		return errors.New("endorser key is not ECDSA")
	}
	msg, err := ns.ASN1Marshal(txID, metadata)
	if err != nil {
		return err
	}
	r, s, err := utils.UnmarshalECDSASignature(e.Endorsement)
	if err != nil {
		return err
	}
	if low, err := utils.IsLowS(pub, s); err != nil || !low {
		return errors.New("signature is not low-S")
	}
	digest := sha256.Sum256(msg)
	if !ecdsa.Verify(pub, digest[:], r, s) {
		return fmt.Errorf("invalid signature on namespace %s", ns.NsId)
	}
	return nil
}

// ─── Registry ────────────────────────────────────────────────────────────────

// Registry holds the identities this process may sign with. Unknown MSPs are never
// created on demand.
type Registry struct {
	defaultMSPID string
	identities   map[string]*Identity
}

func NewRegistry(defaultMSPID string) *Registry {
	return &Registry{defaultMSPID: defaultMSPID, identities: make(map[string]*Identity)}
}

func (r *Registry) Register(id *Identity) {
	r.identities[id.MSPID()] = id
	if r.defaultMSPID == "" {
		r.defaultMSPID = id.MSPID()
	}
}

func (r *Registry) DefaultMSPID() string { return r.defaultMSPID }

// Get returns the identity for mspID.
func (r *Registry) Get(mspID string) (*Identity, bool) {
	id, ok := r.identities[mspID]
	return id, ok
}

// MSPIDs returns the MSPs this process holds keys for.
func (r *Registry) MSPIDs() []string {
	out := make([]string, 0, len(r.identities))
	for id := range r.identities {
		out = append(out, id)
	}
	return out
}

// Len returns the number of registered identities.
func (r *Registry) Len() int { return len(r.identities) }
