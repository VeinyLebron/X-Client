package endorser

import (
	"crypto/x509"
	"encoding/pem"
	"os"
	"path/filepath"
	"testing"

	"github.com/hyperledger/fabric-lib-go/bccsp/utils"
	"github.com/hyperledger/fabric-x-common/api/applicationpb"
)

func testNamespace() *applicationpb.TxNamespace {
	return &applicationpb.TxNamespace{
		NsId:        "cbdc_public",
		BlindWrites: []*applicationpb.Write{{Key: []byte("MOVE_T1"), Value: []byte(`{"movementId":"T1"}`)}},
	}
}

func TestEndorseNamespace_VerifiesAndIsLowS(t *testing.T) {
	id, err := Generate("Org1MSP")
	if err != nil {
		t.Fatal(err)
	}
	ns := testNamespace()
	for range 20 {
		end, err := id.EndorseNamespace(ns, "tx1", nil)
		if err != nil {
			t.Fatal(err)
		}
		if end.Identity.MspId != "Org1MSP" {
			t.Fatalf("identity %s", end.Identity.MspId)
		}
		if err := VerifyEndorsement(ns, "tx1", nil, end); err != nil {
			t.Fatal(err)
		}
		_, s, _ := utils.UnmarshalECDSASignature(end.Endorsement)
		if low, _ := utils.IsLowS(&id.key.PublicKey, s); !low {
			t.Fatal("signature not low-S")
		}
		// The signature binds the transaction ID.
		if err := VerifyEndorsement(ns, "tx2", nil, end); err == nil {
			t.Fatal("signature verified for a different txID")
		}
	}
}

func TestLoad_FailsOnMissingOrMismatchedKey(t *testing.T) {
	dir := t.TempDir()
	a, _ := Generate("Org1MSP")
	b, _ := Generate("Org1MSP")

	certPath := filepath.Join(dir, "cert.pem")
	keyPath := filepath.Join(dir, "priv_sk")
	if err := os.WriteFile(certPath, a.CertPEM(), 0o600); err != nil {
		t.Fatal(err)
	}
	der, _ := x509.MarshalPKCS8PrivateKey(b.key)
	if err := os.WriteFile(keyPath, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}), 0o600); err != nil {
		t.Fatal(err)
	}

	if _, err := Load(Config{MSPID: "Org1MSP", CertPath: certPath, KeyPath: keyPath}); err == nil {
		t.Fatal("mismatched key accepted")
	}
	if _, err := Load(Config{MSPID: "Org1MSP", CertPath: certPath, KeyPath: filepath.Join(dir, "nope")}); err == nil {
		t.Fatal("missing key accepted")
	}

	der, _ = x509.MarshalPKCS8PrivateKey(a.key)
	_ = os.WriteFile(keyPath, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}), 0o600)
	if _, err := Load(Config{MSPID: "Org1MSP", CertPath: certPath, KeyPath: keyPath}); err != nil {
		t.Fatalf("valid identity rejected: %v", err)
	}
}
