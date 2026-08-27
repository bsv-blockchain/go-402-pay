// Package pay402chi provides BRC-121 payment middleware for the Chi web framework.
//
// Usage:
//
//	import pay402chi "github.com/bsv-blockchain/go-402-pay/chi"
//
//	r.Use(pay402chi.PaymentMiddleware(pay402chi.Options{
//	    Wallet:         myWallet,
//	    CalculatePrice: func(path string) int { return 100 },
//	}))
//
//	r.Get("/paid", func(w http.ResponseWriter, r *http.Request) {
//	    result, price, ok := pay402chi.PaymentFromContext(r.Context())
//	    // ...
//	})
package pay402chi

import (
	"context"
	"log/slog"
	"net/http"
	"strconv"

	pay402 "github.com/bsv-blockchain/go-402-pay"
	"github.com/bsv-blockchain/go-sdk/wallet"
)

// contextKey is a custom type for context keys to avoid collisions.
type contextKey string

// paymentContextKey is the key used to store the payment value in context.Context.
const paymentContextKey contextKey = "pay402_payment"

type contextValue struct {
	result *pay402.PaymentResult
	price  int
}

// Options configures the Chi payment middleware.
type Options struct {
	// Wallet is the server's wallet instance.
	Wallet wallet.Interface
	// CalculatePrice returns the price in satoshis for a request path.
	// Return 0 to allow the request through without payment.
	CalculatePrice func(path string) int
	// PaymentWindowMs overrides the default timestamp freshness window.
	// Defaults to pay402.DefaultPaymentWindowMs (30 seconds).
	PaymentWindowMs int
	// Logger is used for payment accept/reject log lines.
	// Defaults to slog.Default().
	Logger *slog.Logger
}

// PaymentMiddleware returns a middleware that enforces BRC-121 payment
// on requests where CalculatePrice returns a non-zero value.
func PaymentMiddleware(opts Options) func(http.Handler) http.Handler {
	log := opts.Logger
	if log == nil {
		log = slog.Default()
	}
	windowMs := opts.PaymentWindowMs
	if windowMs <= 0 {
		windowMs = pay402.DefaultPaymentWindowMs
	}

	var identityKey string

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ctx := r.Context()

			// Lazy identity key fetch
			if identityKey == "" {
				res, err := opts.Wallet.GetPublicKey(ctx, wallet.GetPublicKeyArgs{IdentityKey: true}, "")
				if err != nil {
					log.ErrorContext(ctx, "Failed to get server identity key", "err", err)
					http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
					return
				}
				identityKey = res.PublicKey.ToDERHex()
			}

			price := opts.CalculatePrice(r.URL.Path)
			if price == 0 {
				next.ServeHTTP(w, r)
				return
			}

			if r.Header.Get(pay402.HeaderBeef) == "" {
				send402Chi(w, identityKey, price)
				return
			}

			headers := pay402.PaymentHeaders{
				Sender: r.Header.Get(pay402.HeaderSender),
				Beef:   r.Header.Get(pay402.HeaderBeef),
				Nonce:  r.Header.Get(pay402.HeaderNonce),
				Time:   r.Header.Get(pay402.HeaderTime),
				Vout:   r.Header.Get(pay402.HeaderVout),
			}

			result, err := pay402.ValidatePaymentFromHeaders(ctx, headers, r.URL.Path, opts.Wallet, price, windowMs)
			if err != nil {
				log.ErrorContext(ctx, "Payment rejected", "path", r.URL.Path, "reason", err.Error())
				send402Chi(w, identityKey, price)
				return
			}
			if result == nil {
				send402Chi(w, identityKey, price)
				return
			}

			log.InfoContext(ctx, "Payment accepted",
				"path", r.URL.Path,
				"sats", price,
				"txid", result.TXID,
			)

			ctx = context.WithValue(ctx, paymentContextKey, &contextValue{result: result, price: price})
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// PaymentFromContext retrieves the payment result stored by PaymentMiddleware.
// Returns (result, price, true) on success, or (nil, 0, false) if not present.
func PaymentFromContext(ctx context.Context) (*pay402.PaymentResult, int, bool) {
	v := ctx.Value(paymentContextKey)
	if v == nil {
		return nil, 0, false
	}
	cv, ok := v.(*contextValue)
	if !ok || cv == nil {
		return nil, 0, false
	}
	return cv.result, cv.price, true
}

func send402Chi(w http.ResponseWriter, serverIdentityKey string, sats int) {
	w.Header().Set(pay402.HeaderSats, strconv.Itoa(sats))
	w.Header().Set(pay402.HeaderServer, serverIdentityKey)
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Expose-Headers", pay402.HeaderSats+","+pay402.HeaderServer)
	w.WriteHeader(http.StatusPaymentRequired)
}
