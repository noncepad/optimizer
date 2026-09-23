package brain

import (
	"encoding/binary"

	sgo "github.com/gagliardetto/solana-go"
)

// ChildID builds an sgo.PublicKey-shaped id from a plain uint32 index --
// a copy of sgo.SystemProgramID (32 zero bytes) with index's 4
// big-endian bytes written into the last 4 bytes, e.g. ChildID(1) is 27
// zero bytes followed by 0x00 0x00 0x00 0x01.
//
// Meant for common.DeriveChildKeyV2 (and for harness.Treasury.Child/
// Budget, which key children by this same kind of id) wherever a
// caller wants one child wallet per small integer index -- e.g. one per
// bot uploaded via this package's own Request/UploadRequest, so
// concurrently-running bots never share a signing key.
//
// Deliberately a different convention from
// common.DeriveChildKeyFromIndex's own id-construction (a
// little-endian uint64 written into the FIRST 8 bytes) -- this is not a
// drop-in replacement for that, just a second, explicit option.
func ChildID(index uint32) sgo.PublicKey {
	id := sgo.SystemProgramID
	binary.BigEndian.PutUint32(id[28:32], index)
	return id
}
