package chat

import (
	"errors"
	"time"
)

// Sentinel errors RoomJoin/RoomInvite/RoomSend/Room.Send can return,
// wrapped with room/user-specific detail via fmt.Errorf's %w - check
// with errors.Is. Exists so a caller one layer up (e.g. a gRPC wrapper)
// can map these to specific status codes instead of an opaque failure.
var (
	// ErrRoomNotFound means roomID doesn't exist - either it never did,
	// or (nothing here holds rooms open forever) it did exist but this
	// package's entirely in-memory design means it can't be "looked up
	// again later" the way a durable store's data could.
	ErrRoomNotFound = errors.New("room not found")
	// ErrPermissionDenied means the caller isn't allowed to perform the
	// requested action on this room - RoomInvite by anyone but the
	// room's admin, or RoomJoin by a login that's neither the admin nor
	// invited.
	ErrPermissionDenied = errors.New("permission denied")
	// ErrNotAMember means RoomSend/Room.Send was called for a login
	// that doesn't currently have a live RoomCreate/RoomJoin membership
	// in this room (never joined, or its membership already ended - see
	// RoomJoin's own doc comment on what ends it).
	ErrNotAMember = errors.New("not a member of this room")
)

// Message is one chat message delivered to a Room's RecvC - both other
// members' messages and, for simplicity/consistency across a sender's
// own multiple connections, an echo of the sender's own.
type Message struct {
	From   string
	Text   string
	SentAt time.Time
}

// Invitation is delivered on an InviteListener's InviteC when someone
// invites the listening login to a room.
type Invitation struct {
	RoomID int
	Title  string
	From   string // the admin who sent the invite
}

// InviteListener is the result of RoomListen: InviteC delivers every
// Invitation sent to this login for as long as the ctx passed to
// RoomListen stays alive; ErrorC receives exactly one value (the reason
// listening stopped) when that happens, whether that's ctx being
// canceled, the ChatServer itself shutting down, or (see this package's
// own doc comment) an invite arriving while this listener's buffer was
// already full and had to be dropped - never both, and InviteC is never
// closed, so a caller should select on both rather than only draining
// InviteC.
type InviteListener struct {
	InviteC <-chan Invitation
	ErrorC  <-chan error
}
