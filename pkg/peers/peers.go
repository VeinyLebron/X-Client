// Package peers implements the endorsement protocol between X-Client instances of
// different organizations. The initiating organization signs each request with its MSP
// identity; the receiving organization checks the certificate against the initiator's
// MSP CA before re-executing the operation and co-signing.
package peers

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"time"

	"github.com/hyperledger/fabric-lib-go/bccsp/utils"
	"github.com/hyperledger/fabric-x-common/api/applicationpb"
	"google.golang.org/protobuf/proto"
)

// EndorsePath is the route peers serve endorsement requests on.
const EndorsePath = "/internal/v1/endorse"

// SignatureHeader carries the initiator's signature over the request body.
const SignatureHeader = "X-Endorse-Signature"

// maxBody bounds endorsement request and response sizes.
const maxBody = 4 << 20

// EndorseRequest asks a peer organization to re-execute an operation and endorse the
// namespaces its identity is required for.
type EndorseRequest struct {
	Op           string          `json:"op"`
	Request      json.RawMessage `json:"request"`
	TxID         string          `json:"txId"`
	Timestamp    string          `json:"timestamp"`
	Namespaces   [][]byte        `json:"namespaces"` // proto-encoded applicationpb.TxNamespace, in tx order
	InitiatorMSP string          `json:"initiatorMsp"`
	InitiatorCrt []byte          `json:"initiatorCert"` // PEM
}

// TxNamespaces decodes the proposed namespaces.
func (r *EndorseRequest) TxNamespaces() ([]*applicationpb.TxNamespace, error) {
	out := make([]*applicationpb.TxNamespace, len(r.Namespaces))
	for i, raw := range r.Namespaces {
		ns := &applicationpb.TxNamespace{}
		if err := proto.Unmarshal(raw, ns); err != nil {
			return nil, fmt.Errorf("namespace %d: %w", i, err)
		}
		out[i] = ns
	}
	return out, nil
}

// NamespaceEndorsement is one signature over one namespace.
type NamespaceEndorsement struct {
	Namespace   string `json:"namespace"`
	Endorsement []byte `json:"endorsement"` // proto-encoded applicationpb.EndorsementWithIdentity
}

// EndorseResponse lists the peer's endorsements.
type EndorseResponse struct {
	Endorsements []NamespaceEndorsement `json:"endorsements"`
}

// CodeUnreachable marks an Error for a peer that could not be contacted.
const CodeUnreachable = "PEER_UNREACHABLE"

// Error is a non-2xx answer from a peer, or a failure to reach it.
type Error struct {
	MSPID  string
	Status int
	Code   string
	Msg    string
}

func (e *Error) Error() string {
	if e.Code == CodeUnreachable {
		return fmt.Sprintf("peer %s is unreachable: %s", e.MSPID, e.Msg)
	}
	return fmt.Sprintf("peer %s rejected endorsement (%d %s): %s", e.MSPID, e.Status, e.Code, e.Msg)
}

// Signer signs request bodies.
type Signer interface {
	MSPID() string
	CertPEM() []byte
	Sign(msg []byte) ([]byte, error)
}

// ─── Client ─────────────────────────────────────────────────────────────────

// Client sends endorsement requests to peer organizations.
type Client struct {
	endpoints map[string]string // MSP ID -> base URL
	http      *http.Client
}

func NewClient(endpoints map[string]string, httpClient *http.Client) *Client {
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 15 * time.Second}
	}
	return &Client{endpoints: endpoints, http: httpClient}
}

// Has reports whether a peer endpoint is configured for mspID.
func (c *Client) Has(mspID string) bool {
	_, ok := c.endpoints[mspID]
	return ok
}

// Endorse asks mspID's X-Client to endorse req, signing the request as signer.
func (c *Client) Endorse(ctx context.Context, mspID string, signer Signer, req EndorseRequest) (*EndorseResponse, error) {
	base, ok := c.endpoints[mspID]
	if !ok {
		return nil, fmt.Errorf("no peer endpoint configured for %s", mspID)
	}
	req.InitiatorMSP = signer.MSPID()
	req.InitiatorCrt = signer.CertPEM()
	body, err := json.Marshal(req)
	if err != nil {
		return nil, err
	}
	sig, err := signer.Sign(body)
	if err != nil {
		return nil, err
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, base+EndorsePath, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set(SignatureHeader, base64.StdEncoding.EncodeToString(sig))

	resp, err := c.http.Do(httpReq)
	if err != nil {
		return nil, &Error{MSPID: mspID, Status: http.StatusServiceUnavailable, Code: CodeUnreachable, Msg: err.Error()}
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxBody))
	if err != nil {
		return nil, fmt.Errorf("peer %s: read response: %w", mspID, err)
	}

	if resp.StatusCode != http.StatusOK {
		var e struct {
			Error string `json:"error"`
			Code  string `json:"code"`
		}
		_ = json.Unmarshal(data, &e)
		return nil, &Error{MSPID: mspID, Status: resp.StatusCode, Code: e.Code, Msg: e.Error}
	}
	var out EndorseResponse
	if err := json.Unmarshal(data, &out); err != nil {
		return nil, fmt.Errorf("peer %s: decode response: %w", mspID, err)
	}
	return &out, nil
}

// ─── Verifier ───────────────────────────────────────────────────────────────

// Verifier authenticates incoming endorsement requests.
type Verifier struct {
	roots map[string]*x509.CertPool // MSP ID -> MSP CA certificates
}

// NewVerifier loads one MSP CA certificate file per trusted initiator MSP.
func NewVerifier(caPaths map[string]string) (*Verifier, error) {
	v := &Verifier{roots: make(map[string]*x509.CertPool, len(caPaths))}
	for msp, path := range caPaths {
		caPEM, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("%s MSP CA: %w", msp, err)
		}
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(caPEM) {
			return nil, fmt.Errorf("%s MSP CA %s: no certificates found", msp, path)
		}
		v.roots[msp] = pool
	}
	return v, nil
}

// TrustRoot adds a CA certificate for mspID. Used by tests and in-process setups.
func (v *Verifier) TrustRoot(mspID string, ca *x509.Certificate) {
	pool, ok := v.roots[mspID]
	if !ok {
		pool = x509.NewCertPool()
		v.roots[mspID] = pool
	}
	pool.AddCert(ca)
}

// Verify parses body, checks the initiator certificate chains to its MSP CA, and checks
// the signature in sigHeader over body.
func (v *Verifier) Verify(body []byte, sigHeader string) (*EndorseRequest, error) {
	var req EndorseRequest
	if err := json.Unmarshal(body, &req); err != nil {
		return nil, fmt.Errorf("decode request: %w", err)
	}
	roots, ok := v.roots[req.InitiatorMSP]
	if !ok {
		return nil, fmt.Errorf("initiator MSP %q is not trusted", req.InitiatorMSP)
	}
	block, _ := pem.Decode(req.InitiatorCrt)
	if block == nil {
		return nil, errors.New("initiator certificate is not PEM encoded")
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("initiator certificate: %w", err)
	}
	if _, err := cert.Verify(x509.VerifyOptions{Roots: roots, KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageAny}}); err != nil {
		return nil, fmt.Errorf("initiator certificate is not issued by %s: %w", req.InitiatorMSP, err)
	}

	sig, err := base64.StdEncoding.DecodeString(sigHeader)
	if err != nil || len(sig) == 0 {
		return nil, errors.New("missing or malformed request signature")
	}
	pub, ok := cert.PublicKey.(*ecdsa.PublicKey)
	if !ok {
		return nil, errors.New("initiator key is not ECDSA")
	}
	r, s, err := utils.UnmarshalECDSASignature(sig)
	if err != nil {
		return nil, fmt.Errorf("request signature: %w", err)
	}
	digest := sha256.Sum256(body)
	if !ecdsa.Verify(pub, digest[:], r, s) {
		return nil, errors.New("invalid request signature")
	}
	return &req, nil
}
