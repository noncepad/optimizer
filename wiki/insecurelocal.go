package wiki

import (
	"context"
	"errors"

	sgo "github.com/solana-foundation/solana-go/v2"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

// insecureLocalConnector is a Connector for a plain, unauthenticated local
// gRPC connection - the same dial cmd/wiki-client's own backend connection
// already uses (grpc.NewClient with insecure.NewCredentials()). Unlike
// DefaultConnector, this does no TLS/p2p dialing and holds no private key
// of its own: identity is established upstream of addr, by whatever
// process is actually listening there (e.g. a solpipe relay.SimpleProxy
// forwarding to an authenticated relay under its own key) - this
// connector's caller is trusted the same way any other local process on
// the same host is.
type insecureLocalConnector struct {
	addr string
}

// InsecureLocalConnector connects to addr (host:port) with no TLS and no
// key of its own - use this when addr is already a trusted local endpoint
// (e.g. a relay.SimpleProxy forwarder's -listen address) rather than a
// server this process must itself authenticate to.
func InsecureLocalConnector(addr string) Connector {
	return &insecureLocalConnector{addr: addr}
}

// PrivateKey is unused by insecureLocalConnector - this connector never
// signs or authenticates anything itself, so there is no key to return.
func (ic *insecureLocalConnector) PrivateKey() (sgo.PrivateKey, error) {
	return nil, errors.New("insecureLocalConnector holds no private key: identity comes from whatever is listening at its address, not this process")
}

func (ic *insecureLocalConnector) Dial(ctx context.Context) (*grpc.ClientConn, error) {
	return grpc.NewClient(ic.addr, grpc.WithTransportCredentials(insecure.NewCredentials()))
}
