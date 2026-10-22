package fabricx

import (
	"testing"

	"github.com/hyperledger/fabric-protos-go-apiv2/common"
	"github.com/hyperledger/fabric-x-common/api/applicationpb"
	"github.com/hyperledger/fabric-x-common/protoutil"
	"google.golang.org/protobuf/proto"

	"x-client/pkg/endorser"
)

func TestNewEnvelope_RoundTrip(t *testing.T) {
	id, err := endorser.Generate("Org1MSP")
	if err != nil {
		t.Fatal(err)
	}
	hdr, err := NewHeader(id)
	if err != nil {
		t.Fatal(err)
	}
	if hdr.TxID != protoutil.ComputeTxID(hdr.Nonce, hdr.Creator) {
		t.Fatal("txID is not derived from nonce and creator")
	}

	ns := &applicationpb.TxNamespace{NsId: "cbdc_public", BlindWrites: []*applicationpb.Write{{Key: []byte("k"), Value: []byte("v")}}}
	end, err := id.EndorseNamespace(ns, hdr.TxID, nil)
	if err != nil {
		t.Fatal(err)
	}
	tx := &applicationpb.Tx{
		Namespaces:   []*applicationpb.TxNamespace{ns},
		Endorsements: []*applicationpb.Endorsements{{EndorsementsWithIdentity: []*applicationpb.EndorsementWithIdentity{end}}},
	}
	env, err := NewEnvelope("mychannel", hdr, id, tx)
	if err != nil {
		t.Fatal(err)
	}

	payload := &common.Payload{}
	if err := proto.Unmarshal(env.Payload, payload); err != nil {
		t.Fatal(err)
	}
	chdr := &common.ChannelHeader{}
	if err := proto.Unmarshal(payload.Header.ChannelHeader, chdr); err != nil {
		t.Fatal(err)
	}
	if chdr.Type != int32(common.HeaderType_MESSAGE) || chdr.TxId != hdr.TxID || chdr.ChannelId != "mychannel" {
		t.Fatalf("channel header: %+v", chdr)
	}
	got := &applicationpb.Tx{}
	if err := proto.Unmarshal(payload.Data, got); err != nil {
		t.Fatal(err)
	}
	if err := endorser.VerifyEndorsement(got.Namespaces[0], chdr.TxId, nil, got.Endorsements[0].EndorsementsWithIdentity[0]); err != nil {
		t.Fatalf("endorsement does not survive the round trip: %v", err)
	}

	tx.Endorsements = nil
	if _, err := NewEnvelope("mychannel", hdr, id, tx); err == nil {
		t.Fatal("envelope built without endorsements")
	}
}
