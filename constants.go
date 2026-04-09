// Package pay402 implements BRC-121 Simple 402 Payments — server middleware
// and client helpers for BSV micropayments over HTTP.
//
// https://github.com/bitcoin-sv/BRCs/blob/master/payments/0121.md
package pay402

import "github.com/bsv-blockchain/go-sdk/wallet"

const (
	// HeaderPrefix is the common prefix for all BRC-121 HTTP headers.
	HeaderPrefix = "x-bsv-"

	// Server → Client headers

	// HeaderSats carries the required satoshi amount in a 402 response.
	HeaderSats = "x-bsv-sats"
	// HeaderServer carries the server's identity public key in a 402 response.
	HeaderServer = "x-bsv-server"

	// Client → Server headers

	// HeaderBeef carries the base64-encoded BEEF transaction.
	HeaderBeef = "x-bsv-beef"
	// HeaderSender carries the client's identity public key.
	HeaderSender = "x-bsv-sender"
	// HeaderNonce carries the base64-encoded derivation prefix (8 random bytes).
	HeaderNonce = "x-bsv-nonce"
	// HeaderTime carries the Unix millisecond timestamp as a decimal string.
	HeaderTime = "x-bsv-time"
	// HeaderVout carries the payment output index as a decimal string.
	HeaderVout = "x-bsv-vout"

	// DefaultPaymentWindowMs is the default freshness window for payment timestamps (30 seconds).
	DefaultPaymentWindowMs = 30_000
)

// BRC29ProtocolID is the BRC-42 key derivation protocol used for payment key derivation.
var BRC29ProtocolID = wallet.Protocol{
	SecurityLevel: 2,
	Protocol:      "3241645161d8",
}
