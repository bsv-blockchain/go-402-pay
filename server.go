package pay402

import (
	"context"
	"encoding/base64"
	"fmt"
	"log/slog"
	"math"
	"net/http"
	"strconv"
	"time"

	ec "github.com/bsv-blockchain/go-sdk/primitives/ec"
	"github.com/bsv-blockchain/go-sdk/transaction"
	"github.com/bsv-blockchain/go-sdk/wallet"
)

// PaymentResult is returned by ValidatePayment when a payment is accepted.
type PaymentResult struct {
	SatoshisPaid      uint64
	SenderIdentityKey string
	TXID              string
}

// PaymentError is returned by ValidatePayment when the payment is a detected replay.
// Unlike a nil return (missing/malformed headers), a PaymentError carries a loggable reason.
type PaymentError struct {
	Reason string
}

func (e *PaymentError) Error() string { return e.Reason }

// PaymentHeaders carries the raw BRC-121 header values extracted from any HTTP framework.
// This decouples validation logic from net/http so framework adapters can use it directly.
type PaymentHeaders struct {
	Sender string // x-bsv-sender
	Beef   string // x-bsv-beef  (base64)
	Nonce  string // x-bsv-nonce (base64)
	Time   string // x-bsv-time  (unix ms as decimal string)
	Vout   string // x-bsv-vout  (decimal string)
}

// Send402 writes a 402 Payment Required response carrying the required satoshi
// amount and the server identity key.
func Send402(w http.ResponseWriter, serverIdentityKey string, sats int) {
	w.Header().Set(HeaderSats, strconv.Itoa(sats))
	w.Header().Set(HeaderServer, serverIdentityKey)
	w.WriteHeader(http.StatusPaymentRequired)
}

// ValidatePaymentFromHeaders validates BRC-121 payment from pre-extracted header values.
// This is the framework-agnostic validation core used by all middleware adapters.
//
// Returns:
//   - (*PaymentResult, nil) — payment is valid and accepted.
//   - (nil, *PaymentError) — payment is a replay; the error carries a loggable reason.
//   - (nil, nil)           — headers are missing or malformed; respond with 402.
func ValidatePaymentFromHeaders(
	ctx context.Context,
	h PaymentHeaders,
	requestPath string,
	w wallet.Interface,
	requiredSats int,
	paymentWindowMs int,
) (*PaymentResult, error) {
	if paymentWindowMs <= 0 {
		paymentWindowMs = DefaultPaymentWindowMs
	}

	if h.Sender == "" || h.Beef == "" || h.Nonce == "" || h.Time == "" || h.Vout == "" {
		return nil, nil
	}

	// Validate timestamp freshness
	timestamp, err := strconv.ParseInt(h.Time, 10, 64)
	if err != nil {
		return nil, nil
	}
	nowMs := time.Now().UnixMilli()
	if math.Abs(float64(nowMs-timestamp)) > float64(paymentWindowMs) {
		return nil, nil
	}

	// Decode BEEF
	beefBytes, err := base64.StdEncoding.DecodeString(h.Beef)
	if err != nil {
		return nil, nil
	}

	// Parse BEEF and extract the payment transaction
	_, tx, txHash, err := transaction.ParseBeef(beefBytes)
	if err != nil || tx == nil {
		return nil, nil
	}
	txid := txHash.String()

	// Verify the specified output carries at least the required satoshi amount
	voutIndex, err := strconv.ParseUint(h.Vout, 10, 32)
	if err != nil {
		return nil, nil
	}
	if int(voutIndex) >= len(tx.Outputs) {
		return nil, nil
	}
	output := tx.Outputs[voutIndex]
	if output.Satoshis < uint64(requiredSats) {
		return nil, nil
	}

	// Decode the sender identity key to pass to InternalizeAction
	senderKey, err := ec.PublicKeyFromString(h.Sender)
	if err != nil {
		return nil, nil
	}

	// derivationSuffix = base64(utf8(timeStr)) — matches the TypeScript implementation
	derivationSuffix := []byte(h.Time)
	derivationPrefix, err := base64.StdEncoding.DecodeString(h.Nonce)
	if err != nil {
		return nil, nil
	}

	result, err := w.InternalizeAction(ctx, wallet.InternalizeActionArgs{
		Tx:          beefBytes,
		Description: fmt.Sprintf("Payment for %s", requestPath),
		Outputs: []wallet.InternalizeOutput{
			{
				OutputIndex: uint32(voutIndex),
				Protocol:    wallet.InternalizeProtocolWalletPayment,
				PaymentRemittance: &wallet.Payment{
					DerivationPrefix:  derivationPrefix,
					DerivationSuffix:  derivationSuffix,
					SenderIdentityKey: senderKey,
				},
			},
		},
	}, "")
	if err != nil {
		return nil, nil
	}

	if !result.Accepted {
		return nil, &PaymentError{
			Reason: fmt.Sprintf("Replayed transaction: txid %s has already been processed", txid),
		}
	}

	return &PaymentResult{
		SatoshisPaid:      output.Satoshis,
		SenderIdentityKey: h.Sender,
		TXID:              txid,
	}, nil
}

// ValidatePayment validates the BRC-121 payment headers on an incoming net/http request.
// It is a convenience wrapper around ValidatePaymentFromHeaders.
func ValidatePayment(
	ctx context.Context,
	r *http.Request,
	w wallet.Interface,
	requiredSats int,
	paymentWindowMs int,
) (*PaymentResult, error) {
	return ValidatePaymentFromHeaders(ctx, PaymentHeaders{
		Sender: r.Header.Get(HeaderSender),
		Beef:   r.Header.Get(HeaderBeef),
		Nonce:  r.Header.Get(HeaderNonce),
		Time:   r.Header.Get(HeaderTime),
		Vout:   r.Header.Get(HeaderVout),
	}, r.URL.Path, w, requiredSats, paymentWindowMs)
}

// MiddlewareOptions configures the payment middleware.
type MiddlewareOptions struct {
	// Wallet is the server's wallet instance.
	Wallet wallet.Interface
	// CalculatePrice returns the price in satoshis for a request path.
	// Return 0 to allow the request through without payment.
	CalculatePrice func(path string) int
	// PaymentWindowMs overrides the default timestamp freshness window.
	// Defaults to DefaultPaymentWindowMs (30 seconds).
	PaymentWindowMs int
	// Logger is used for payment accept/reject log lines.
	// Defaults to slog.Default().
	Logger *slog.Logger
}

// PaymentMiddleware returns an http.Handler that enforces BRC-121 payment on
// requests where CalculatePrice returns a non-zero value.
//
// On success the payment result is stored in the request context and retrievable
// via PaymentFromContext. The next handler is called with the enriched context.
func PaymentMiddleware(opts MiddlewareOptions, next http.Handler) http.Handler {
	log := opts.Logger
	if log == nil {
		log = slog.Default()
	}
	windowMs := opts.PaymentWindowMs
	if windowMs <= 0 {
		windowMs = DefaultPaymentWindowMs
	}

	var identityKey string

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()

		// Lazy identity key fetch — cached after first successful call
		if identityKey == "" {
			res, err := opts.Wallet.GetPublicKey(ctx, wallet.GetPublicKeyArgs{IdentityKey: true}, "")
			if err != nil {
				log.ErrorContext(ctx, "Failed to get server identity key", "err", err)
				http.Error(w, "internal server error", http.StatusInternalServerError)
				return
			}
			identityKey = res.PublicKey.ToDERHex()
		}

		price := opts.CalculatePrice(r.URL.Path)
		if price == 0 {
			next.ServeHTTP(w, r)
			return
		}

		if r.Header.Get(HeaderBeef) == "" {
			Send402(w, identityKey, price)
			return
		}

		result, err := ValidatePayment(ctx, r, opts.Wallet, price, windowMs)
		if err != nil {
			log.ErrorContext(ctx, "Payment rejected", "path", r.URL.Path, "reason", err.Error())
			Send402(w, identityKey, price)
			return
		}
		if result == nil {
			Send402(w, identityKey, price)
			return
		}

		log.InfoContext(ctx, "Payment accepted",
			"path", r.URL.Path,
			"sats", price,
			"txid", result.TXID,
		)

		next.ServeHTTP(w, r.WithContext(contextWithPayment(ctx, result, price)))
	})
}
