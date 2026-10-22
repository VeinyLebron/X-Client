// Package fabricx assembles endorsed Fabric-X transactions into signed envelopes and
// submits them for ordering.
package fabricx

import (
	"fmt"

	"github.com/hyperledger/fabric-protos-go-apiv2/common"
	"github.com/hyperledger/fabric-x-common/api/applicationpb"
	"github.com/hyperledger/fabric-x-common/protoutil"
	"google.golang.org/protobuf/proto"
)

// Signer signs messages and serializes its identity as a Fabric SerializedIdentity.
type Signer interface {
	Sign(msg []byte) ([]byte, error)
	Serialize() ([]byte, error)
}

// Header binds a transaction ID to its creator, as in Fabric: txID = SHA-256(nonce ‖ creator).
type Header struct {
	TxID    string
	Nonce   []byte
	Creator []byte
}

// NewHeader creates a fresh transaction header for creator.
func NewHeader(creator Signer) (Header, error) {
	serialized, err := creator.Serialize()
	if err != nil {
		return Header{}, err
	}
	nonce, err := protoutil.CreateNonce()
	if err != nil {
		return Header{}, err
	}
	return Header{TxID: protoutil.ComputeTxID(nonce, serialized), Nonce: nonce, Creator: serialized}, nil
}

// NewEnvelope wraps a fully endorsed transaction in a MESSAGE envelope signed by the
// creator. The orderer routes it and the committer validates it.
func NewEnvelope(channel string, h Header, creator Signer, tx *applicationpb.Tx) (*common.Envelope, error) {
	if len(tx.Endorsements) != len(tx.Namespaces) {
		return nil, fmt.Errorf("transaction has %d namespaces but %d endorsement sets", len(tx.Namespaces), len(tx.Endorsements))
	}
	data, err := proto.Marshal(tx)
	if err != nil {
		return nil, fmt.Errorf("marshal tx: %w", err)
	}

	chdr := protoutil.MakeChannelHeader(common.HeaderType_MESSAGE, 0, channel, 0)
	chdr.TxId = h.TxID
	payload, err := proto.Marshal(&common.Payload{
		Header: protoutil.MakePayloadHeader(chdr, protoutil.MakeSignatureHeader(h.Creator, h.Nonce)),
		Data:   data,
	})
	if err != nil {
		return nil, fmt.Errorf("marshal payload: %w", err)
	}

	sig, err := creator.Sign(payload)
	if err != nil {
		return nil, fmt.Errorf("sign envelope: %w", err)
	}
	return &common.Envelope{Payload: payload, Signature: sig}, nil
}
