# Configuration

X-Client reads one YAML file, passed with `-config` (default `config/config.yaml`). This branch ships:

| File | Use |
|---|---|
| `config/config.yaml` | standalone: in-memory ledger, both orgs in one process, `:8080` |
| `config/org1.yaml` | Org1 against the devnet, `:8080` |
| `config/org2.yaml` | Org2 against the devnet, `:8081` |

Relative paths (keys, certificates) are resolved from the working directory: the repo root when run on the host, `/app` in the container.

## Environment variables

**Inside the file**, `${VAR}` and `${VAR:-default}` are replaced before parsing. The shipped configs use this so one file serves both host and Docker (`docker-compose.2org.yaml` sets the variables):

| Variable | In | Host default |
|---|---|---|
| `PORT` | `server.port` | 8080 / 8081 |
| `API_TOKEN` | `server.api_token` | empty |
| `QS_ENDPOINT`, `SIDECAR_ENDPOINT` | `query_service.endpoint`, `sidecar.endpoint` | `localhost:7001`/`4001` (Org1), `7002`/`4002` (Org2) |
| `ROUTER1`…`ROUTER4` | `orderers[].endpoint` | `localhost:7050`, `7150`, `7250`, `7350` |
| `PEER_ORG1`, `PEER_ORG2` | `peers.<MSP>.endpoint` | `http://localhost:8080`, `http://localhost:8081` |

**After parsing**, these always override the file: `PORT`, `MODE`, `DEFAULT_MSP_ID`, `QUERY_SERVICE_ENDPOINT`, `API_TOKEN`. If the config file does not exist, X-Client starts on the defaults plus these overrides.

## Keys

| Key | Default | Meaning |
|---|---|---|
| `server.host`, `server.port` | `0.0.0.0`, `8080` | REST listen address |
| `server.api_token` | empty | if set, `/api/...` requires `Authorization: Bearer <token>`. `/healthz` and the peer endpoint do not use it |
| `mode` | `in-memory` | `grpc` = live Fabric-X network; `in-memory` = standalone ledger |
| `channel_id` | `mychannel` | channel in the envelope and block subscription |
| `central_bank_msp` | `CentralBankMSP` (the shipped configs set `Org1MSP`) | the issuer: creates/freezes wallets, mints, reads supply, co-signs by default |
| `default_msp_id` | = `central_bank_msp` | the organization this instance acts as. Requests act as this MSP |
| `allow_caller_header` | `false` | if `true`, the `X-Caller-MSP` header picks any MSP this process holds a key for. Only for one trusted process holding several orgs' keys (standalone) |
| `endorsers.<MSP>.cert_path`, `.key_path` | – | signing identity (PEM certificate, PKCS#8 or SEC1 EC key). In `grpc` mode the key for `default_msp_id` is required, and a missing or mismatched key stops startup. In `in-memory` mode, MSPs without files get throwaway keys |
| `query_service.endpoint`, `.tls` | – | this org's Query Service (point reads, namespace versions, tx status) |
| `sidecar.endpoint`, `.tls` | – | this org's sidecar (block stream for the key index) |
| `orderers[].endpoint`, `.tls` | – | Arma routers. Every envelope goes to all of them, and a majority must accept |
| `commit_timeout` | `30s` | how long to wait for the commit status before answering `202 SUBMITTED` |
| `peers.<MSP>.endpoint` | – | base URL of that org's X-Client, used to request co-signatures |
| `peers.<MSP>.msp_ca` | – | that org's MSP CA certificate. Requests to this node's `/internal/v1/endorse` are accepted only from MSPs listed here, signed by a certificate issued by this CA |
| `namespaces.public`, `.central_bank_pdc`, `.credits` | `cbdc_public`, `cbdc_cb_pdc`, `cbdc_credits` | namespace names |
| `namespaces.banks.<MSP>` | `Org1MSP: cbdc_org1_pdc`, `Org2MSP: cbdc_org2_pdc` | balance namespace per bank. The MSPs listed here are the banks |
| `endorsement_policies.<namespace>` | derived (below) | MSPs that must all sign that namespace. Overrides the default for that namespace only |

Default endorsement policies: the issuer for `public`, `central_bank_pdc` and `credits`; for each bank's balance namespace, `[bank, issuer]` (just `[issuer]` when the bank is the issuer). They must match the on-chain policies. See [POLICIES.md](POLICIES.md).

## TLS

Each `tls` block takes:

| Field | Meaning |
|---|---|
| `mode` | `none`, `tls` (verify the server) or `mtls` (also present a client certificate) |
| `ca_cert_paths` | CA certificates that may sign the server's certificate (required for `tls`/`mtls`) |
| `cert_path`, `key_path` | client certificate and key (`mtls` only) |
| `server_name` | override the name checked in the server certificate. Defaults to the endpoint host |

The devnet requires `mtls` everywhere. A committer trusts client certificates from its own org's TLS CA (`crypto/<org>/tls/client.crt`). The routers trust every application org's TLS CA and present certificates from the four orderer TLS CAs (`crypto/orderer-tls/`). Server certificates are always verified.

Peer-to-peer X-Client traffic (`peers.*.endpoint`) is plain HTTP in the shipped configs. Requests are authenticated by their MSP signature, not by TLS. Put the endpoints on a private network, or behind TLS (use `https://` URLs), if they cross untrusted links.

## Host vs Docker

| | Host (`make run-org1`) | Docker (`docker-compose.2org.yaml`) |
|---|---|---|
| Org1 committer | `localhost:7001`, `localhost:4001` | `committer-query-service:7001`, `committer-sidecar:4001` |
| Org2 committer | `localhost:7002`, `localhost:4002` | `committer-org2-query-service:7001`, `committer-org2-sidecar:4001` |
| Routers | `localhost:7050`… | `orderer-party1-router:7050`… |
| Peers | `http://localhost:8080` / `8081` | `http://x-client-org1:8080` / `http://x-client-org2:8081` |

The devnet's server certificates include both `localhost` and the container names, so TLS verifies either way.
