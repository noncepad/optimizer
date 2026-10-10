// Package wiki
package wiki

import (
	"context"
	"fmt"

	pbw "git.noncepad.com/pkg/optimizer/gen/wiki/v1"
	sgo "github.com/solana-foundation/solana-go/v2"
	"google.golang.org/grpc"
)

type Client struct {
	ctx    context.Context
	cancel context.CancelCauseFunc
	conn   *grpc.ClientConn
	wiki   pbw.WikiServiceClient
}

func (c *Client) Ctx() context.Context {
	return c.ctx
}

// Conn returns the underlying *grpc.ClientConn this Client was built over,
// so a caller can construct another service client (e.g.
// chatclient.New(c.Conn())) sharing the same connection instead of dialing
// twice - gRPC multiplexes services over one connection by name, so there
// is no conflict.
func (c *Client) Conn() *grpc.ClientConn {
	return c.conn
}

type Connector interface {
	PrivateKey() (sgo.PrivateKey, error)
	Dial(context.Context) (*grpc.ClientConn, error)
}

func Connect(ctx context.Context, connector Connector) (*Client, error) {
	c := new(Client)
	c.ctx, c.cancel = context.WithCancelCause(ctx)
	var err error
	c.conn, err = connector.Dial(c.ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to connect: %s", err)
	}
	c.wiki = pbw.NewWikiServiceClient(c.conn)
	return c, nil
}
