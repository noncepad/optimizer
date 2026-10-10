// Package chatclient is a real gRPC client for chat.v1.ChatService (see
// chatserver's own doc comment for the server side, and
// proto/chat/v1/chat.proto for the RPC surface itself).
//
// Every method expects ctx to already carry the caller's identity via
// the "USER" gRPC metadata key (see grpcauth) - the same convention
// every other client in this repo follows (e.g. cmd/wiki-client's
// -login flag, attached once via
// metadata.AppendToOutgoingContext(ctx, grpcauth.MetadataKey, login));
// this package never adds that metadata itself.
//
// ListenForInvites/JoinRoom return channel-based handles shaped like
// chat.InviteListener/chat.Room (reusing chat.Invitation/chat.Message
// directly) so code written against an in-process chat.ChatServer looks
// almost the same calling through this package instead - the one real
// difference is that these channels are closed when the underlying
// stream ends (network/server issue, ctx canceled, ...), where
// chat.Room/chat.InviteListener's own channels are never closed by
// design (see their own doc comments).
package chat

import (
	"context"
	"fmt"
	"strings"

	chatv1 "git.noncepad.com/pkg/optimizer/gen/chat/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// Client is a thin wrapper around a chat.v1.ChatServiceClient bound to
// an existing *grpc.ClientConn - it does not dial, own, or close the
// connection itself, so a caller sharing one grpc.ClientConn across
// several service clients (e.g. WikiService and ChatService on the same
// connection) can construct a Client without any extra setup.
type Client struct {
	c chatv1.ChatServiceClient
}

// New wraps conn as a Client. conn may already be connected to any of
// chat.v1.ChatService's siblings (e.g. wiki.v1.WikiService) - gRPC
// multiplexes services over one connection by name, so there is no
// conflict.
func New(conn *grpc.ClientConn) *Client {
	return &Client{c: chatv1.NewChatServiceClient(conn)}
}

// RoomInfo is CreateRoomResponse's Go-side shape.
type RoomInfo struct {
	ID    uint64
	Title string
}

// statusToChatErr maps a gRPC status error back to chat's own sentinel
// errors (chat.ErrRoomNotFound/ErrPermissionDenied/ErrNotAMember - see
// chatserver's chatErrToStatus, the inverse of this) where possible, so
// calling code can use errors.Is the same way whether it's talking to
// an in-process chat.ChatServer or a remote one through this package.
// Any other status code (or a non-status error, e.g. a transport
// failure) is returned unchanged.
func statusToChatErr(err error) error {
	if err == nil {
		return nil
	}
	st, ok := status.FromError(err)
	if !ok {
		return err
	}
	switch st.Code() {
	case codes.NotFound:
		return fmt.Errorf("%s: %w", trimSentinelSuffix(st.Message(), ErrRoomNotFound), ErrRoomNotFound)
	case codes.PermissionDenied:
		return fmt.Errorf("%s: %w", trimSentinelSuffix(st.Message(), ErrPermissionDenied), ErrPermissionDenied)
	case codes.FailedPrecondition:
		return fmt.Errorf("%s: %w", trimSentinelSuffix(st.Message(), ErrNotAMember), ErrNotAMember)
	default:
		return err
	}
}

// trimSentinelSuffix strips a trailing ": <sentinel>" from msg, if
// present - chatserver's chatErrToStatus passes chat's own error text
// straight through as the gRPC status message, and that text already
// ends with the sentinel's own generic wording (chat's errors are all
// built as fmt.Errorf("...: %w", sentinel)), so wrapping it with the
// same sentinel again here would otherwise read as "detail: reason:
// reason".
func trimSentinelSuffix(msg string, sentinel error) string {
	return strings.TrimSuffix(msg, ": "+sentinel.Error())
}

// CreateRoom creates a new room titled title, with ctx's authenticated
// caller as its admin.
func (cl *Client) CreateRoom(ctx context.Context, title string) (RoomInfo, error) {
	resp, err := cl.c.CreateRoom(ctx, &chatv1.CreateRoomRequest{Title: title})
	if err != nil {
		return RoomInfo{}, statusToChatErr(err)
	}
	return RoomInfo{ID: resp.GetRoomId(), Title: resp.GetTitle()}, nil
}

// Invite invites invitee to roomID - ctx's authenticated caller must be
// that room's admin (see chat.ErrPermissionDenied).
func (cl *Client) Invite(ctx context.Context, roomID uint64, invitee string) error {
	_, err := cl.c.Invite(ctx, &chatv1.InviteRequest{RoomId: roomID, Invitee: invitee})
	return statusToChatErr(err)
}

// ListenForInvites subscribes to invitations sent to ctx's authenticated
// caller, for as long as ctx stays alive (canceling it ends the
// underlying stream, same as chat.RoomListen's own ctx). Even a failure
// to start the stream at all (e.g. Unauthenticated) is delivered on
// ErrorC rather than as a second return value, so callers only need to
// select on InviteC/ErrorC regardless of which stage failed.
func (cl *Client) ListenForInvites(ctx context.Context) InviteListener {
	inviteC := make(chan Invitation, 10)
	errorC := make(chan error, 1)

	stream, err := cl.c.ListenForInvites(ctx, &chatv1.ListenForInvitesRequest{})
	if err != nil {
		errorC <- statusToChatErr(err)
		close(inviteC)
		return InviteListener{InviteC: inviteC, ErrorC: errorC}
	}

	go func() {
		defer close(inviteC)
		for {
			inv, err := stream.Recv()
			if err != nil {
				errorC <- statusToChatErr(err)
				return
			}
			select {
			case inviteC <- Invitation{RoomID: int(inv.GetRoomId()), Title: inv.GetTitle(), From: inv.GetFrom()}:
			default: // best-effort, matching chat package's own drop-on-full-buffer policy
			}
		}
	}()

	return InviteListener{InviteC: inviteC, ErrorC: errorC}
}

// Room mirrors chat.Room - a live connection to a chat room, obtained
// from JoinRoom. See this package's own doc comment on the one real
// difference (RecvC closes when the stream ends).
type Room struct {
	ID     uint64
	RecvC  <-chan Message
	ErrorC <-chan error

	cl *Client
}

// Send broadcasts text to every current member of r's room, including
// r's own caller (self-echo) - a separate, stateless RPC from whatever
// JoinRoom call established membership, so ctx here need not be (and
// need not use) the same context JoinRoom was called with, only carry
// the same authenticated identity.
func (r Room) Send(ctx context.Context, text string) error {
	_, err := r.cl.c.SendMessage(ctx, &chatv1.SendMessageRequest{RoomId: r.ID, Text: text})
	return statusToChatErr(err)
}

// JoinRoom joins roomID as ctx's authenticated caller - who must be
// that room's admin or have been invited (chat.ErrPermissionDenied
// otherwise) - returning a live Room handle. Membership (and so the
// ability to call Room.Send for this room) lasts exactly as long as ctx
// stays alive, same as chat.RoomJoin's own ctx; canceling it ends the
// underlying stream and closes RecvC.
func (cl *Client) JoinRoom(ctx context.Context, roomID uint64) (Room, error) {
	stream, err := cl.c.JoinRoom(ctx, &chatv1.JoinRoomRequest{RoomId: roomID})
	if err != nil {
		return Room{}, statusToChatErr(err)
	}

	recvC := make(chan Message, 100)
	errorC := make(chan error, 1)
	go func() {
		defer close(recvC)
		for {
			msg, err := stream.Recv()
			if err != nil {
				errorC <- statusToChatErr(err)
				return
			}
			select {
			case recvC <- Message{From: msg.GetFrom(), Text: msg.GetText(), SentAt: msg.GetSentAt().AsTime()}:
			default: // best-effort, matching chat package's own drop-on-full-buffer policy
			}
		}
	}()

	return Room{ID: roomID, RecvC: recvC, ErrorC: errorC, cl: cl}, nil
}

// SendMessage is Room.Send without needing a held Room value - only
// roomID and ctx's authenticated caller's current membership matter,
// same relationship chat.RoomSend has to chat.Room.Send.
func (cl *Client) SendMessage(ctx context.Context, roomID uint64, text string) error {
	_, err := cl.c.SendMessage(ctx, &chatv1.SendMessageRequest{RoomId: roomID, Text: text})
	return statusToChatErr(err)
}
