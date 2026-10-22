# Org1MSP crypto material

Populated by `./scripts/copy-crypto.sh` from `fabric-x-samples/devnet-3org/crypto/peerOrganizations/org1.example.com`:

| File | Source | Used for |
|---|---|---|
| `cert.pem`, `priv_sk` | `users/User1@org1.example.com/msp/signcerts`, `keystore` | endorsements and envelope signatures |
| `tls/ca.crt` | `users/User1@org1.example.com/tls/ca.crt` | verifying this org's committer (Query Service, sidecar) |
| `tls/client.crt`, `tls/client.key` | `users/User1@org1.example.com/tls/` | mTLS to the committer and the Arma routers |

The public MSP CA of every organization goes to `crypto/cas/` and the orderer TLS CAs to `crypto/orderer-tls/`. All of it is git-ignored.
