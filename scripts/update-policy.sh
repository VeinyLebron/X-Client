#!/usr/bin/env bash
# Change the endorsement policy of an existing namespace on an X-Setup (or fabric-x-samples) network.
# Runs `fxconfig namespace update` the same way the devnet Makefile runs `namespace create`:
# as Org1's User1, endorsed by Org1 (the devnet LifecycleEndorsement policy accepts it).
#
#   ./scripts/update-policy.sh <devnet-dir> <namespace> "<policy>"
#   ./scripts/update-policy.sh ../X-Setup/3-org cbdc_public "AND('CentralBankMSP.member')"
#
# The current namespace version is looked up automatically. Afterwards set the same rule under
# endorsement_policies in every X-Client config.

set -euo pipefail

if [ $# -ne 3 ]; then
    sed -n '2,8p' "$0"
    exit 1
fi
DEVNET_DIR="$(cd "$1" && pwd)" NS="$2" POLICY="$3"
TOOLS_IMAGE="${TOOLS_IMAGE:-ghcr.io/hyperledger/fabric-x-tools:1.0.1}"
NETWORK="${NETWORK:-fabric-x}"
ORG="$DEVNET_DIR/crypto/peerOrganizations/org1.example.com"

fxconfig() {
    docker run --rm --network "$NETWORK" --user "$(id -u):$(id -g)" \
        -v "$DEVNET_DIR/fxconfig.yaml:/config/fxconfig.yaml:ro,Z" \
        -v "$ORG/peers/fxconfig.org1.example.com/tls:/tls:ro,Z" \
        -v "$ORG/users/User1@org1.example.com/msp:/msp:ro,Z" \
        -v "$ORG/msp/tlscacerts/tlsca.org1.example.com-cert.pem:/org-tls-ca.pem:ro,Z" \
        -v "$DEVNET_DIR/crypto/ordererOrganizations/orderer-org-1/msp/tlscacerts/tlsca.orderer-org-1-cert.pem:/orderer-tls-ca.pem:ro,Z" \
        "$TOOLS_IMAGE" fxconfig "$@" --config=/config/fxconfig.yaml
}

VERSION="$(fxconfig namespace list 2>/dev/null | sed -n "s/^[0-9]*) $NS: version \([0-9]*\) .*/\1/p")"
if [ -z "$VERSION" ]; then
    echo "Error: namespace $NS is not installed." >&2
    exit 1
fi

echo "Updating $NS (version $VERSION) to policy: $POLICY"
fxconfig namespace update "$NS" --policy="$POLICY" --version="$VERSION" --endorse --submit --wait >/dev/null 2>&1
fxconfig namespace list 2>/dev/null | grep ") $NS:"
