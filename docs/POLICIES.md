# Changing endorsement policies

This guide explains how the endorsement rules work and how to change them: who must sign what, new banks, namespace names. Recipes use the 3-org network `X-Setup/3-org` (CentralBankMSP, Org1MSP = Bank A, Org2MSP = Bank B).

## 1. Concepts

- **Policies apply per namespace, not per key.** To give two kinds of data different signing rules, put them in different namespaces. That is why the CBDC data is split into 5 namespaces. For example, pending credits live in `cbdc_credits` (Central Bank only), so a payee bank does not have to sign incoming payments.
- **Policy syntax** (Fabric signature policies):
  - `AND('Org1MSP.member','CentralBankMSP.member')`: both must sign
  - `OR('Org1MSP.member','Org2MSP.member')`: either may sign
  - `OutOf(2, 'CentralBankMSP.member','Org1MSP.member','Org2MSP.member')`: any 2 of 3
- **A transaction needs a valid endorsement set for every namespace it writes.** A Bank A → Bank B transfer writes `cbdc_org1_pdc`, `cbdc_credits` and `cbdc_public`, so all three policies must be satisfied.

The default layout on this branch:

| Namespace | Policy | Why |
|---|---|---|
| `cbdc_public` | `AND('CentralBankMSP.member')` | Central Bank controls wallet records and the movement log |
| `cbdc_cb_pdc` | `AND('CentralBankMSP.member')` | supply is the Central Bank's ledger |
| `cbdc_credits` | `AND('CentralBankMSP.member')` | payments can arrive without the payee online |
| `cbdc_org1_pdc` | `AND('Org1MSP.member','CentralBankMSP.member')` | Bank A controls its balance; the Central Bank co-signs |
| `cbdc_org2_pdc` | `AND('Org2MSP.member','CentralBankMSP.member')` | Bank B likewise |

## 2. Two places that must agree

| Where | Set by | Effect |
|---|---|---|
| **On-chain policy** | `fxconfig namespace create/update` (X-Setup/3-org: `make init-namespace-cbdc`, `scripts/update-policy.sh`) | The committer enforces it. This is what actually protects the ledger. |
| **Client policy** | `endorsement_policies` in each X-Client config, or the defaults (Central Bank on everything, plus the owning bank on its balance namespace) | Which signatures X-Client collects before submitting. |

- **Client asks for less than on-chain**: every affected transaction is aborted with `ABORTED_SIGNATURE_INVALID` (HTTP 502). Nothing is written.
- **Client asks for more than on-chain**: it works, but collects extra signatures it does not need, and fails if that org is down.
- **All X-Clients must use the same client policy.** A co-signer only signs the namespaces *its* config says it is required for. If the Central Bank's config omits a requirement that Bank A's config has, Bank A's request fails with `503 ENDORSEMENT_UNAVAILABLE`.
- **A peer must be configured for every MSP whose signature is required**, under `peers:` in the initiator's config (endpoint and MSP CA). The co-signer needs the initiator under its own `peers:` to accept the request.

The startup log prints the client policy in effect (`Policy cbdc_org1_pdc : AND[Org1MSP CentralBankMSP]`). Compare it with `make list-namespaces` in X-Setup/3-org, which shows each policy hex-encoded with its version.

## 3. Changing an existing namespace's policy

Namespaces can be updated in place; each update increases their version.

```bash
# 1. on-chain (looks up the current version, submits, waits for commit)
./scripts/update-policy.sh ../X-Setup/3-org \
    cbdc_public "AND('CentralBankMSP.member','Org1MSP.member','Org2MSP.member')"

# 2. client: the same rule in ALL THREE configs (centralbank.yaml, bank_a.yaml, bank_b.yaml)
endorsement_policies:
  cbdc_public: [CentralBankMSP, Org1MSP, Org2MSP]

# 3. every org that may now be asked to sign needs the others under peers:
#    (by default the banks only list the Central Bank)

# 4. restart the X-Clients
```

Between steps 1 and 4, transactions touching that namespace fail with `ABORTED_SIGNATURE_INVALID`. X-Client picks up the new namespace version by itself (cached for at most 30 s; a restart refreshes it immediately).

`scripts/update-policy.sh` runs `fxconfig namespace update` as Org1's User1. That works because X-Setup/3-org's `LifecycleEndorsement` is `OR('Org1MSP.member','Org2MSP.member','CentralBankMSP.member')`. On a network with a stricter lifecycle policy, produce the update with `--output=tx.json` and collect the extra endorsements before submitting.

## 4. Recipes

### 4.1 Payee bank must accept incoming payments
Make credits require the payee by moving them into the payee's balance namespace. This is a contract change: in `pkg/contract/cbdc.go`, `MintCBDC` and `TransferCBDC` write the credit with `sim.PutJSON(c.ns.Credits, …)`, and `pendingCredits`/`consolidate` read and delete it there. Use `c.BalanceNamespace(payee.OwnerMSP)` instead. Each transfer then needs payer, payee and Central Bank. Add every bank to every other bank's `peers:`.

### 4.2 Any two of three organizations
On-chain `OutOf(2, 'CentralBankMSP.member','Org1MSP.member','Org2MSP.member')`. The client only collects "all of these" (see §5), so list the two it should ask, e.g. `[Org1MSP, CentralBankMSP]`, in every config.

### 4.3 Remove the Central Bank as co-signer of bank balances
```bash
./scripts/update-policy.sh "$DEVNET" cbdc_org1_pdc "AND('Org1MSP.member')"
./scripts/update-policy.sh "$DEVNET" cbdc_org2_pdc "AND('Org2MSP.member')"
```
```yaml
endorsement_policies:          # all three configs
  cbdc_org1_pdc: [Org1MSP]
  cbdc_org2_pdc: [Org2MSP]
```
Transfers still need the Central Bank for `cbdc_public` and `cbdc_credits`. The Central Bank remains the notary of the movement log; it just cannot change bank balances any more.

### 4.4 Add a third bank (Org3)
Network (in a copy of `X-Setup/3-org`, the same way it added `CentralBank` to the upstream devnet):
1. Add an `Org3` peer org to `crypto-config.yaml` and `configtx.yaml` (`Application.Organizations`, `LifecycleEndorsement`), `make init`, and add a committer stack (copy `compose.org2.yaml`, rename services, new host ports).
2. Register `cbdc_org3_pdc` with `AND('Org3MSP.member','CentralBankMSP.member')`.

Client:
1. `config/bank_c.yaml` (copy `bank_b.yaml`): `default_msp_id: Org3MSP`, its keys, its committer ports, peer `CentralBankMSP`.
2. In **every** config, add `Org3MSP: "cbdc_org3_pdc"` under `namespaces.banks`.
3. In `config/centralbank.yaml`, add Org3 under `peers` so the Central Bank accepts its requests.
4. Extend `scripts/copy-crypto.sh` with `copy_org bank_c org3.example.com Org3MSP`.

### 4.5 Change who the issuer is
On `main` the issuer is fixed to `CentralBankMSP`. The `two-org` branch makes it configurable (`central_bank_msp`); port that change (`pkg/contract`, `engine.DefaultPolicies`, `cmd/server/config.go`) if you need it here.

### 4.6 Rename or move namespaces
Register the new namespaces, then point every config at them (`namespaces.public`, `.central_bank_pdc`, `.credits`, `.banks`). Data is not migrated: new namespaces start empty.

## 5. Limits and extension points

- **The client models every policy as "all of these MSPs must sign"** (`engine.Policies` in `pkg/engine/engine.go`). `AND(...)` maps directly. For `OR(...)`/`OutOf(n, ...)` on-chain, list the subset X-Client should collect. Collecting "any n of m that respond" needs a change in `Policies.Required` and `collectEndorsements`, which today fail if any listed MSP does not answer.
- **Adding a contract operation** that co-signers can re-execute:
  1. add an `Op…` constant and a case in `CBDCService.Execute` (`pkg/contract/cbdc.go`). Peers dispatch through the same function, so this is all they need;
  2. use only `TxContext` for time and IDs, never `time.Now()` or randomness, or co-signers will compute different bytes (`PROPOSAL_MISMATCH`);
  3. add a route in `registerRoutes` (`pkg/api/handlers.go`) calling `s.invoke(w, r, contract.OpX, req)`.
- **Reads in namespaces a transaction does not write are not MVCC-protected**, because Fabric-X rejects namespaces without writes. Keep facts that can change (wallet status, balances) in a namespace the operation also writes.
