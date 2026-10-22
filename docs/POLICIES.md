# Changing endorsement policies

This guide explains how the endorsement rules work and how to change them: who must sign what, the issuer, new banks, namespace names. Every recipe was written against the 2-org devnet (Org1, Org2).

## 1. Concepts

- **Policies apply per namespace, not per key.** To give two kinds of data different signing rules, put them in different namespaces. That is why the CBDC data is split into 5 namespaces. For example, pending credits live in `cbdc_credits` (issuer only), so a payee does not have to sign incoming payments.
- **Policy syntax** (Fabric signature policies):
  - `AND('Org1MSP.member','Org2MSP.member')`: both must sign
  - `OR('Org1MSP.member','Org2MSP.member')`: either may sign
  - `OutOf(2, 'Org1MSP.member','Org2MSP.member','Org3MSP.member')`: any 2 of 3
- **A transaction needs a valid endorsement set for every namespace it writes.** A transfer that writes `cbdc_org2_pdc`, `cbdc_credits` and `cbdc_public` must satisfy all three policies.

The default layout on this branch:

| Namespace | Policy | Why |
|---|---|---|
| `cbdc_public` | `AND('Org1MSP.member')` | issuer controls wallet records and the movement log |
| `cbdc_cb_pdc` | `AND('Org1MSP.member')` | supply is the issuer's ledger |
| `cbdc_credits` | `AND('Org1MSP.member')` | payments can arrive without the payee online |
| `cbdc_org1_pdc` | `AND('Org1MSP.member')` | Org1's own balances |
| `cbdc_org2_pdc` | `AND('Org2MSP.member','Org1MSP.member')` | Org2 controls its balance; the issuer co-signs |

## 2. Two places that must agree

| Where | Set by | Effect |
|---|---|---|
| **On-chain policy** | `fxconfig namespace create/update` (devnet: `make init-namespace`, `scripts/update-policy.sh`) | The committer enforces it. This is what actually protects the ledger. |
| **Client policy** | `endorsement_policies` in each X-Client config, or the defaults derived from `central_bank_msp` + `namespaces.banks` | Which signatures X-Client collects before submitting. |

- **Client asks for less than on-chain**: every affected transaction is aborted with `ABORTED_SIGNATURE_INVALID` (HTTP 502). Nothing is written.
- **Client asks for more than on-chain**: it works, but collects extra signatures it does not need, and fails if that org is down.
- **All X-Clients must use the same client policy.** A co-signer only signs the namespaces *its* config says it is required for. If Org2's config does not list Org2 for a namespace, Org2 will not sign it, and Org1's request fails with `ENDORSEMENT_UNAVAILABLE`.

The startup log prints the client policy in effect:
```
 Policy cbdc_org2_pdc : AND[Org2MSP Org1MSP]
```
Compare it with `make list-namespaces` in the devnet. That command shows each policy encoded as hex; the version increases with every update.

## 3. Changing an existing namespace's policy

Namespaces can be updated in place; each update increases their version.

```bash
# 1. on-chain (looks up the current version, submits, waits for commit)
./scripts/update-policy.sh ~/Projects/repos-bench/fabric-x-samples/devnet \
    cbdc_public "AND('Org1MSP.member','Org2MSP.member')"
# → 4) cbdc_public: version 1 policy: 1228…

# 2. client: add the same rule to BOTH config/org1.yaml and config/org2.yaml
endorsement_policies:
  cbdc_public: [Org1MSP, Org2MSP]

# 3. restart both X-Clients
```

Between steps 1 and 3, transactions touching that namespace fail with `ABORTED_SIGNATURE_INVALID`. Change the policy when traffic is quiet, or stop the clients first. X-Client picks up the new namespace version by itself (it caches versions for at most 30 s; a restart refreshes them immediately).

`scripts/update-policy.sh` runs `fxconfig namespace update` as Org1's User1. That works because the devnet's `LifecycleEndorsement` policy is `OR('Org1MSP.member','Org2MSP.member')`. On a network with a stricter lifecycle policy, produce the update with `--output=tx.json` and collect the extra endorsements before submitting.

## 4. Recipes

### 4.1 Both organizations sign everything
For when neither org should be able to act alone, not even the issuer.

```bash
for ns in cbdc_public cbdc_cb_pdc cbdc_credits cbdc_org1_pdc cbdc_org2_pdc; do
  ./scripts/update-policy.sh "$DEVNET" $ns "AND('Org1MSP.member','Org2MSP.member')"
done
```
```yaml
# config/org1.yaml and config/org2.yaml
endorsement_policies:
  cbdc_public:   [Org1MSP, Org2MSP]
  cbdc_cb_pdc:   [Org1MSP, Org2MSP]
  cbdc_credits:  [Org1MSP, Org2MSP]
  cbdc_org1_pdc: [Org1MSP, Org2MSP]
  cbdc_org2_pdc: [Org2MSP, Org1MSP]
```
Every operation now needs both X-Clients online. Org1's transactions are co-signed by Org2's X-Client, which re-executes them first.

### 4.2 The payee must accept incoming payments
Make credits require the payee by moving them into the payee's balance namespace. This is a contract change: in `pkg/contract/cbdc.go`, `MintCBDC` and `TransferCBDC` write the credit with `sim.PutJSON(c.ns.Credits, …)`, and `pendingCredits`/`consolidate` read and delete it there. Write and read `c.BalanceNamespace(payee.OwnerMSP)` instead. The payee's balance policy then applies to every incoming payment, so the payee's X-Client must be online to receive. No on-chain change is needed. `cbdc_credits` simply stops being written.

### 4.3 Change who the issuer is
To make Org2 the issuer:
```yaml
# both configs
central_bank_msp: "Org2MSP"
```
```bash
for ns in cbdc_public cbdc_cb_pdc cbdc_credits; do
  ./scripts/update-policy.sh "$DEVNET" $ns "AND('Org2MSP.member')"
done
./scripts/update-policy.sh "$DEVNET" cbdc_org1_pdc "AND('Org1MSP.member','Org2MSP.member')"
./scripts/update-policy.sh "$DEVNET" cbdc_org2_pdc "AND('Org2MSP.member')"
```
The default client policies follow `central_bank_msp` automatically. Existing wallets keep their owners; only who may mint, create and freeze changes.

### 4.4 Add a third bank (Org3)
Network (in a copy of the devnet; see the `main` branch, which does this for a Central Bank org in `devnet-3org`):
1. Add an `Org3` peer org to `crypto-config.yaml` and `configtx.yaml` (`Application.Organizations`, `LifecycleEndorsement`), then `make init` and start a committer stack for it (copy `compose.org2.yaml`, rename the services, use new host ports).
2. Register `cbdc_org3_pdc` with `AND('Org3MSP.member','Org1MSP.member')`.

Client:
1. `config/org3.yaml` (copy `org2.yaml`): `default_msp_id: Org3MSP`, its keys, its committer ports, peer `Org1MSP`.
2. In **every** config, add `Org3MSP: "cbdc_org3_pdc"` under `namespaces.banks`.
3. In `config/org1.yaml`, add Org3 under `peers` (endpoint and `crypto/cas/Org3MSP.pem`) so Org1 accepts Org3's requests.
4. Extend `scripts/copy-crypto.sh` with `copy_org org3 org3.example.com Org3MSP`.

### 4.5 Rename or move namespaces
Register the new namespaces, then point every config at them:
```yaml
namespaces:
  public: "bank_public"
  central_bank_pdc: "bank_supply"
  credits: "bank_credits"
  banks: { Org1MSP: "bank_org1", Org2MSP: "bank_org2" }
```
Data is not migrated: new namespaces start empty.

## 5. Limits and extension points

- **The client models every policy as "all of these MSPs must sign"** (`engine.Policies`, [pkg/engine/engine.go:440](../pkg/engine/engine.go#L440)). `AND(...)` on-chain maps directly. For `OR(...)` or `OutOf(n, ...)` on-chain, list the subset you want X-Client to collect (e.g. `[Org1MSP]` for `OR('Org1MSP.member','Org2MSP.member')`). Collecting "any n of m that respond" needs a code change in `Policies.Required` ([engine.go:464](../pkg/engine/engine.go#L464)) and in `collectEndorsements` ([engine.go:206](../pkg/engine/engine.go#L206)), which today fails if any listed MSP does not answer.
- **Adding a contract operation** that co-signers can re-execute:
  1. add an `Op…` constant and a case in `CBDCService.Execute` ([pkg/contract/cbdc.go:138](../pkg/contract/cbdc.go#L138)). Peers dispatch through the same function, so this is all they need;
  2. use only `TxContext` for time and IDs, never `time.Now()` or randomness, or co-signers will compute different bytes (`PROPOSAL_MISMATCH`);
  3. add a route in `registerRoutes` ([pkg/api/handlers.go:60](../pkg/api/handlers.go#L60)) that calls `s.invoke(w, r, contract.OpX, req)`.
- **Reads in namespaces a transaction does not write are not MVCC-protected**, because Fabric-X rejects namespaces without writes. Keep facts that can change (wallet status, balances) in a namespace the operation also writes, as the existing operations do.
