package engine_test

import (
	"context"
	"slices"
	"testing"

	"x-client/pkg/contract"
	"x-client/pkg/engine"
	"x-client/pkg/models"
	"x-client/pkg/simulator"
)

// twoOrgService is the 2-org layout: Org1 issues CBDC and is also a bank; Org2 is a bank.
func twoOrgService() *contract.CBDCService {
	ns := contract.DefaultNamespaces()
	ns.CentralBankMSP = bankA
	ns.Banks = map[string]string{bankA: "cbdc_org1_pdc", bankB: "cbdc_org2_pdc"}
	return contract.NewCBDCService(ns)
}

func TestTwoOrg_Policies(t *testing.T) {
	p := engine.DefaultPolicies(twoOrgService(), banks)
	want := map[string][]string{
		"cbdc_public":   {bankA},
		"cbdc_cb_pdc":   {bankA},
		"cbdc_credits":  {bankA},
		"cbdc_org1_pdc": {bankA},
		"cbdc_org2_pdc": {bankB, bankA},
	}
	for ns, msps := range want {
		if got := p[ns]; !slices.Equal(got, msps) {
			t.Errorf("%s: got %v, want %v", ns, got, msps)
		}
	}
}

func TestTwoOrg_IssuerIsAlsoABank(t *testing.T) {
	svc := twoOrgService()
	nodes, _, mem := newNetworkWith(t, svc, engine.DefaultPolicies(svc, banks), bankA, bankB)
	org1, org2 := nodes[bankA].engine, nodes[bankB].engine
	ctx := context.Background()

	invoke(t, org1, bankA, contract.OpCreateWallet, models.CreateWalletRequest{WalletID: "WA", OwnerMSP: bankA, ParticipantType: models.ParticipantBank})
	invoke(t, org1, bankA, contract.OpCreateWallet, models.CreateWalletRequest{WalletID: "WB", OwnerMSP: bankB, ParticipantType: models.ParticipantBank})
	invoke(t, org1, bankA, contract.OpMint, models.MintCBDCRequest{WalletID: "WA", MovementID: "M1", Amount: 1_000_000, Salt: salt})

	_, err := org2.Invoke(ctx, bankB, contract.OpMint, models.MintCBDCRequest{WalletID: "WB", MovementID: "M2", Amount: 1, Salt: salt}, true)
	expectCode(t, err, contract.ErrAccessDenied)

	// Org1 paying Org2 needs only Org1's signature.
	res := invoke(t, org1, bankA, contract.OpTransfer, models.TransferCBDCRequest{FromWalletID: "WA", ToWalletID: "WB", MovementID: "T1", Amount: 300_000, Salt: salt})
	for _, e := range res.Endorsements {
		if e.EndorserMSP != bankA {
			t.Fatalf("unexpected endorser %s on %s", e.EndorserMSP, e.Namespace)
		}
	}

	// Org2's operations on its own balance need Org1's co-signature.
	res = invoke(t, org2, bankB, contract.OpConsolidate, models.ConsolidateRequest{WalletID: "WB"})
	var org1Signed bool
	for _, e := range res.Endorsements {
		org1Signed = org1Signed || (e.Namespace == "cbdc_org2_pdc" && e.EndorserMSP == bankA)
	}
	if !org1Signed {
		t.Fatalf("Org1 did not co-sign cbdc_org2_pdc: %+v", res.Endorsements)
	}
	invoke(t, org2, bankB, contract.OpTransfer, models.TransferCBDCRequest{FromWalletID: "WB", ToWalletID: "WA", MovementID: "T2", Amount: 100_000, Salt: salt})
	invoke(t, org1, bankA, contract.OpConsolidate, models.ConsolidateRequest{WalletID: "WA"})

	if bv := balance(t, svc, mem, bankA, "WA"); bv.Settled != 800_000 {
		t.Fatalf("WA = %+v", bv)
	}
	if bv := balance(t, svc, mem, bankB, "WB"); bv.Settled != 200_000 {
		t.Fatalf("WB = %+v", bv)
	}
	sim := simulator.NewMultiNamespaceSimulator(ctx, mem)
	if sv, err := svc.QueryTotalSupply(sim, bankA); err != nil || sv.TotalSupply != 1_000_000 {
		t.Fatalf("supply %+v %v", sv, err)
	}
}
