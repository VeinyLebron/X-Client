#!/usr/bin/env bash
# Copy the crypto material X-Client needs from the 3-org network (after `make up` / `make init`).
# Source: the argument, else ../X-Setup/3-org next to this repo, else fabric-x-samples/devnet-3org.
#
# Per organization (crypto/<org>/):
#   cert.pem, priv_sk          signing identity (User1)       -> endorsements, envelopes
#   tls/ca.crt                 the org's TLS CA               -> verify its committer (QS + sidecar)
#   tls/client.crt, client.key the user's TLS client keypair  -> mTLS to committer and routers
# Shared, public only:
#   cas/<MSP>.pem              each org's MSP CA              -> authenticate peer X-Client requests
#   orderer-tls/*.pem          orderer TLS CAs                -> verify the Arma routers

set -euo pipefail

REPO_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
if [ -n "${1:-}" ]; then
    DEVNET_DIR="$1"
elif [ -d "$REPO_DIR/../X-Setup/3-org" ]; then
    DEVNET_DIR="$REPO_DIR/../X-Setup/3-org"
else
    DEVNET_DIR="$HOME/Projects/repos-bench/fabric-x-samples/devnet-3org"
fi
CRYPTO_DIR="$DEVNET_DIR/crypto"

if [ ! -d "$CRYPTO_DIR/peerOrganizations/centralbank.example.com" ]; then
    echo "Error: 3-org crypto not found at $CRYPTO_DIR (run 'make up' in X-Setup/3-org first)."
    echo "Usage: ./scripts/copy-crypto.sh [path-to-X-Setup/3-org or fabric-x-samples/devnet-3org]"
    exit 1
fi

DEST_DIR="$REPO_DIR/crypto"
echo "Copying crypto material from: $CRYPTO_DIR"
echo "Destination directory:        $DEST_DIR"

mkdir -p "$DEST_DIR/cas" "$DEST_DIR/orderer-tls"

copy_org() {
    local dir="$1" domain="$2" msp="$3"
    local org="$CRYPTO_DIR/peerOrganizations/$domain"
    local user="$org/users/User1@$domain"
    echo "  $msp -> crypto/$dir"

    mkdir -p "$DEST_DIR/$dir/tls"
    # Files written by earlier versions of this script.
    rm -f "$DEST_DIR/$dir/ca.crt" "$DEST_DIR/$dir/tls_client.crt" "$DEST_DIR/$dir/tls_client.key"

    cp "$user"/msp/signcerts/*.pem "$DEST_DIR/$dir/cert.pem"
    cp "$user"/msp/keystore/*_sk   "$DEST_DIR/$dir/priv_sk"
    cp "$user/tls/ca.crt"          "$DEST_DIR/$dir/tls/ca.crt"
    cp "$user/tls/client.crt"      "$DEST_DIR/$dir/tls/client.crt"
    cp "$user/tls/client.key"      "$DEST_DIR/$dir/tls/client.key"
    chmod 600 "$DEST_DIR/$dir/priv_sk" "$DEST_DIR/$dir/tls/client.key"

    cp "$org/msp/cacerts/"*.pem "$DEST_DIR/cas/$msp.pem"
}

copy_org centralbank centralbank.example.com CentralBankMSP
copy_org bank_a      org1.example.com        Org1MSP
copy_org bank_b      org2.example.com        Org2MSP

for n in 1 2 3 4; do
    cp "$CRYPTO_DIR/ordererOrganizations/orderer-org-$n/msp/tlscacerts/tlsca.orderer-org-$n-cert.pem" \
       "$DEST_DIR/orderer-tls/orderer-org-$n.pem"
done

echo "All crypto material copied."
