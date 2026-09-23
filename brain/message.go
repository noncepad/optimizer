package brain

import (
	"fmt"

	sgo "github.com/gagliardetto/solana-go"
	"github.com/noncepad/catmsg"
)

// KeyFlagWallet is the one wire-protocol key flag every bot mode in
// this codebase already shares byte-for-byte (see brain/arbv1,
// brain/testperpv1, brain/testperplatencyv1, brain/perpfundingv1,
// brain/helloworldv1, brain/leveragedloopv1, and brain/multimodelv1's
// own message.go, each with an identical KeyFlagWallet = 3 and DoWallet
// of their own) -- unlike higher-numbered, per-strategy key flags, this
// one is universal: every WASM bot needs a signing key sent to it
// exactly this way before it can do anything real. Kept here, once,
// rather than making every caller of this package duplicate it again.
const KeyFlagWallet uint8 = 3

// DoWallet builds the stdin message (send it via Bot.SendC) that gives
// a bot key as its own trading wallet -- see KeyFlagWallet's own doc
// comment. Panics on a malformed key, same as every per-mode DoWallet
// this mirrors: a wrong-length ed25519 private key here is a caller
// bug, not a runtime condition to recover from.
func DoWallet(key sgo.PrivateKey) catmsg.FixedPair {
	if len(key) != 64 {
		panic(fmt.Errorf("brain: DoWallet: bad key length: %d != 64", len(key)))
	}
	var x catmsg.FixedPair
	if err := x.From([]byte{KeyFlagWallet}, key[:]); err != nil {
		panic(err)
	}
	return x
}
