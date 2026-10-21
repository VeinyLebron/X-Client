package contract

import (
	"context"
	"testing"

	"x-client/pkg/endorser"
	"x-client/pkg/models"
	"x-client/pkg/simulator"
	"x-client/pkg/store"
)

const (
	cbMSP   = models.CentralBankMSP
	bankA   = "Org1MSP"
	bankB   = "Org2MSP"
	testSalt = "0123456789abcdef-salt"
)

func setupTestEnvironment(t *testing.T) (*CBDCService, *store.InMemoryStore, *endorser.Endorser) {
	svc := NewCBDCService()
	st := store.NewInMemoryStore()
	end, err := endorser.New(endorser.Config{MSPID: bankA})
	if err != nil {
		t.Fatalf("failed to create endorser: %v", err)
	}
	return svc, st, end
}

func TestCBDC_FullLifecycle(t *testing.T) {
	svc, st, end := setupTestEnvironment(t)
	ctx := context.Background()

	// 1. CreateWallet WA (Org1MSP) and WB (Org2MSP)
	{
		sim := simulator.NewMultiNamespaceSimulator(ctx, st)
		_, err := svc.CreateWallet(sim, bankA, "tx1", models.CreateWalletRequest{
			WalletID:        "WA",
			OwnerMSP:        bankA,
			ParticipantType: models.ParticipantBank,
		})
		if err == nil {
			t.Fatal("expected access denied for non-centralbank")
		}

		w, err := svc.CreateWallet(sim, cbMSP, "tx1", models.CreateWalletRequest{
			WalletID:        "WA",
			OwnerMSP:        bankA,
			ParticipantType: models.ParticipantBank,
		})
		if err != nil {
			t.Fatalf("create wallet WA: %v", err)
		}
		if w.Status != models.WalletActive {
			t.Fatalf("wallet status: %s", w.Status)
		}

		rwSets, err := sim.BuildReadWriteSets()
		if err != nil {
			t.Fatalf("build rwsets: %v", err)
		}
		if _, ok := rwSets[models.DefaultPublicNamespace]; !ok {
			t.Fatal("expected public namespace write")
		}
		if _, ok := rwSets[models.PDCNameForMSP(bankA)]; !ok {
			t.Fatal("expected Org1MSP PDC namespace write")
		}

		// Apply to in-memory store
		st.Set(models.DefaultPublicNamespace, walletKey("WA"), []byte(`{"walletId":"WA","ownerMsp":"Org1MSP","status":"ACTIVE"}`))
		st.Set(models.PDCNameForMSP(bankA), balanceKey("WA"), []byte(`{"walletId":"WA","balance":0}`))

		// Create WB
		st.Set(models.DefaultPublicNamespace, walletKey("WB"), []byte(`{"walletId":"WB","ownerMsp":"Org2MSP","status":"ACTIVE"}`))
		st.Set(models.PDCNameForMSP(bankB), balanceKey("WB"), []byte(`{"walletId":"WB","balance":0}`))
	}

	// 2. MintCBDC 1,000,000 into WA
	{
		sim := simulator.NewMultiNamespaceSimulator(ctx, st)
		mv, err := svc.MintCBDC(sim, cbMSP, "tx2", models.MintCBDCRequest{
			WalletID:   "WA",
			MovementID: "M1",
			Amount:     1000000,
			Salt:       testSalt,
		})
		if err != nil {
			t.Fatalf("mint failed: %v", err)
		}
		if mv.AmountHash == "" {
			t.Fatal("expected amount hash")
		}

		rwSets, err := sim.BuildReadWriteSets()
		if err != nil {
			t.Fatalf("build rwsets: %v", err)
		}

		sig, err := end.SignReadWriteSet(rwSets)
		if err != nil {
			t.Fatalf("sign rwsets: %v", err)
		}
		if sig.Signature == "" {
			t.Fatal("empty signature")
		}

		// Apply credit and movement to store
		st.Set(models.PDCNameForMSP(bankA), creditKey("WA", "M1"), []byte(`{"walletId":"WA","movementId":"M1","amount":1000000}`))
		st.Set(models.DefaultCentralBankPDC, supplyKey("M1"), []byte(`{"movementId":"M1","delta":1000000}`))
		st.Set(models.DefaultPublicNamespace, movementKey("M1"), []byte(`{"movementId":"M1","amountHash":"`+mv.AmountHash+`"}`))
	}

	// 3. CheckBalance on WA
	{
		sim := simulator.NewMultiNamespaceSimulator(ctx, st)
		bv, err := svc.CheckBalance(sim, bankA, "WA")
		if err != nil {
			t.Fatalf("check balance WA: %v", err)
		}
		if bv.Available != 1000000 || bv.PendingCredits != 1000000 {
			t.Fatalf("expected 1000000 available and pending credits, got %+v", bv)
		}
	}

	// 4. Transfer 300,000 from WA to WB
	{
		sim := simulator.NewMultiNamespaceSimulator(ctx, st)
		mv, err := svc.TransferCBDC(sim, bankA, "tx3", models.TransferCBDCRequest{
			FromWalletID: "WA",
			ToWalletID:   "WB",
			MovementID:   "T1",
			Amount:       300000,
			Salt:         testSalt,
		})
		if err != nil {
			t.Fatalf("transfer failed: %v", err)
		}

		rwSets, err := sim.BuildReadWriteSets()
		if err != nil {
			t.Fatalf("build rwsets: %v", err)
		}

		// Check that WA's PDC has ReadWrite for balance and WB's PDC has BlindWrite for credit
		org1Set := rwSets[models.PDCNameForMSP(bankA)]
		if len(org1Set.ReadWrites) == 0 {
			t.Fatal("expected ReadWrite on sender balance")
		}
		org2Set := rwSets[models.PDCNameForMSP(bankB)]
		if len(org2Set.BlindWrites) == 0 {
			t.Fatal("expected BlindWrite on receiver credit")
		}

		// Update store: WA settled balance becomes 700000, pending credit deleted
		st.Delete(models.PDCNameForMSP(bankA), creditKey("WA", "M1"))
		st.Set(models.PDCNameForMSP(bankA), balanceKey("WA"), []byte(`{"walletId":"WA","balance":700000}`))
		st.Set(models.PDCNameForMSP(bankB), creditKey("WB", "T1"), []byte(`{"walletId":"WB","movementId":"T1","amount":300000}`))
		st.Set(models.DefaultPublicNamespace, movementKey("T1"), []byte(`{"movementId":"T1","amountHash":"`+mv.AmountHash+`"}`))
	}

	// 5. ConsolidateBalance on WB
	{
		sim := simulator.NewMultiNamespaceSimulator(ctx, st)
		bal, err := svc.ConsolidateBalance(sim, bankB, "tx4", "WB")
		if err != nil {
			t.Fatalf("consolidate WB failed: %v", err)
		}
		if bal != 300000 {
			t.Fatalf("expected consolidated balance 300000, got %d", bal)
		}

		// Update store: WB balance = 300000, credit deleted
		st.Delete(models.PDCNameForMSP(bankB), creditKey("WB", "T1"))
		st.Set(models.PDCNameForMSP(bankB), balanceKey("WB"), []byte(`{"walletId":"WB","balance":300000}`))
	}

	// 6. FreezeWallet WB and check transfer rejection
	{
		sim := simulator.NewMultiNamespaceSimulator(ctx, st)
		_, err := svc.FreezeWallet(sim, cbMSP, "WB", "compliance check")
		if err != nil {
			t.Fatalf("freeze WB failed: %v", err)
		}
		st.Set(models.DefaultPublicNamespace, walletKey("WB"), []byte(`{"walletId":"WB","ownerMsp":"Org2MSP","status":"FROZEN"}`))

		// Transfer to WB should fail
		sim2 := simulator.NewMultiNamespaceSimulator(ctx, st)
		_, err = svc.TransferCBDC(sim2, bankA, "tx5", models.TransferCBDCRequest{
			FromWalletID: "WA",
			ToWalletID:   "WB",
			MovementID:   "T2",
			Amount:       50000,
			Salt:         testSalt,
		})
		if err == nil {
			t.Fatal("expected failure transferring to frozen wallet")
		}

		// Unfreeze WB
		sim3 := simulator.NewMultiNamespaceSimulator(ctx, st)
		_, err = svc.UnfreezeWallet(sim3, cbMSP, "WB")
		if err != nil {
			t.Fatalf("unfreeze WB failed: %v", err)
		}
		st.Set(models.DefaultPublicNamespace, walletKey("WB"), []byte(`{"walletId":"WB","ownerMsp":"Org2MSP","status":"ACTIVE"}`))
	}

	// 7. VerifyMovementAmount on T1
	{
		sim := simulator.NewMultiNamespaceSimulator(ctx, st)
		verified, err := svc.VerifyMovementAmount(sim, "T1", 300000, testSalt)
		if err != nil || !verified {
			t.Fatalf("verify movement amount failed: verified=%v err=%v", verified, err)
		}
		wrong, _ := svc.VerifyMovementAmount(sim, "T1", 299999, testSalt)
		if wrong {
			t.Fatal("wrong amount should not verify")
		}
	}

	// 8. QueryTotalSupply
	{
		sim := simulator.NewMultiNamespaceSimulator(ctx, st)
		sup, err := svc.QueryTotalSupply(sim, cbMSP)
		if err != nil {
			t.Fatalf("query total supply failed: %v", err)
		}
		if sup.TotalSupply != 1000000 {
			t.Fatalf("expected total supply 1000000, got %d", sup.TotalSupply)
		}
	}
}
