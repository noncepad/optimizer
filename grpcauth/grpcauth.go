// Package grpcauth makes the caller's identity, carried in the "USER"
// gRPC metadata key, available to RPC handlers via context.Context.
//
// This package only extracts and propagates whatever the "USER" metadata
// key says - it does not authenticate anything itself. It assumes
// something upstream of this server (a reverse proxy, an API gateway,
// mTLS termination, etc.) has already verified the caller and sets that
// metadata key to their login name before the request reaches here.
package grpcauth

import (
	"context"

	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"
)

// MetadataKey is the gRPC metadata key carrying the caller's login name.
// gRPC metadata keys are matched case-insensitively.
const MetadataKey = "USER"

type userKey struct{}

// FromContext returns the authenticated login name attached to ctx by
// UnaryServerInterceptor/StreamServerInterceptor, and whether one was
// present. Handlers that require an authenticated caller should treat
// ok == false as unauthenticated.
func FromContext(ctx context.Context) (login string, ok bool) {
	v, ok := ctx.Value(userKey{}).(string)
	if !ok || v == "" {
		return "", false
	}
	return v, true
}

// NewContext returns a copy of ctx with login attached, as if it had
// arrived via the "USER" gRPC metadata key. Intended for tests.
func NewContext(ctx context.Context, login string) context.Context {
	return context.WithValue(ctx, userKey{}, login)
}

func userFromIncomingContext(ctx context.Context) string {
	md, ok := metadata.FromIncomingContext(ctx)
	if !ok {
		return ""
	}
	vals := md.Get(MetadataKey)
	if len(vals) == 0 {
		return ""
	}
	return vals[0]
}

// UnaryServerInterceptor extracts the "USER" gRPC metadata key from each
// incoming unary request and makes it available to the handler via
// FromContext.
func UnaryServerInterceptor(ctx context.Context, req any, _ *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
	if user := userFromIncomingContext(ctx); user != "" {
		ctx = NewContext(ctx, user)
	}
	return handler(ctx, req)
}

type serverStream struct {
	grpc.ServerStream
	ctx context.Context
}

func (s *serverStream) Context() context.Context { return s.ctx }

// StreamServerInterceptor is the streaming-RPC equivalent of
// UnaryServerInterceptor.
func StreamServerInterceptor(srv any, ss grpc.ServerStream, _ *grpc.StreamServerInfo, handler grpc.StreamHandler) error {
	ctx := ss.Context()
	if user := userFromIncomingContext(ctx); user != "" {
		ctx = NewContext(ctx, user)
	}
	return handler(srv, &serverStream{ServerStream: ss, ctx: ctx})
}
