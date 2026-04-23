// Package pay402gin provides BRC-121 payment middleware for the Gin web framework.
//
// Usage:
//
//	import pay402gin "github.com/bsv-blockchain/go-402-pay/gin"
//
//	r.Use(pay402gin.PaymentMiddleware(pay402gin.Options{
//	    Wallet:         myWallet,
//	    CalculatePrice: func(path string) int { return 100 },
//	}))
//
//	r.GET("/paid", func(c *gin.Context) {
//	    result, price, ok := pay402gin.PaymentFromContext(c)
//	    // ...
//	})
package pay402gin

import (
	"context"
	"log/slog"
	"net/http"
	"strconv"

	pay402 "github.com/bsv-blockchain/go-402-pay"
	"github.com/bsv-blockchain/go-sdk/wallet"
	"github.com/gin-gonic/gin"
)

// paymentContextKey is the key used to store the payment value in gin.Context.
const paymentContextKey = "pay402_payment"

type contextValue struct {
	result *pay402.PaymentResult
	price  int
}

// Options configures the Gin payment middleware.
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

// PaymentMiddleware returns a Gin handler function that enforces BRC-121 payment
// on requests where CalculatePrice returns a non-zero value.
func PaymentMiddleware(opts Options) gin.HandlerFunc {
	log := opts.Logger
	if log == nil {
		log = slog.Default()
	}
	windowMs := opts.PaymentWindowMs
	if windowMs <= 0 {
		windowMs = pay402.DefaultPaymentWindowMs
	}

	var identityKey string

	return func(c *gin.Context) {
		ctx := context.Background()

		// Lazy identity key fetch
		if identityKey == "" {
			res, err := opts.Wallet.GetPublicKey(ctx, wallet.GetPublicKeyArgs{IdentityKey: true}, "")
			if err != nil {
				log.ErrorContext(ctx, "Failed to get server identity key", "err", err)
				c.AbortWithStatus(http.StatusInternalServerError)
				return
			}
			identityKey = res.PublicKey.ToDERHex()
		}

		price := opts.CalculatePrice(c.Request.URL.Path)
		if price == 0 {
			c.Next()
			return
		}

		if c.GetHeader(pay402.HeaderBeef) == "" {
			send402Gin(c, identityKey, price)
			return
		}

		headers := pay402.PaymentHeaders{
			Sender: c.GetHeader(pay402.HeaderSender),
			Beef:   c.GetHeader(pay402.HeaderBeef),
			Nonce:  c.GetHeader(pay402.HeaderNonce),
			Time:   c.GetHeader(pay402.HeaderTime),
			Vout:   c.GetHeader(pay402.HeaderVout),
		}

		result, err := pay402.ValidatePaymentFromHeaders(ctx, headers, c.Request.URL.Path, opts.Wallet, price, windowMs)
		if err != nil {
			log.ErrorContext(ctx, "Payment rejected", "path", c.Request.URL.Path, "reason", err.Error())
			send402Gin(c, identityKey, price)
			return
		}
		if result == nil {
			send402Gin(c, identityKey, price)
			return
		}

		log.InfoContext(ctx, "Payment accepted",
			"path", c.Request.URL.Path,
			"sats", price,
			"txid", result.TXID,
		)

		c.Set(paymentContextKey, &contextValue{result: result, price: price})
		c.Next()
	}
}

// PaymentFromContext retrieves the payment result stored by PaymentMiddleware.
// Returns (result, price, true) on success, or (nil, 0, false) if not present.
func PaymentFromContext(c *gin.Context) (*pay402.PaymentResult, int, bool) {
	v, exists := c.Get(paymentContextKey)
	if !exists {
		return nil, 0, false
	}
	cv, ok := v.(*contextValue)
	if !ok || cv == nil {
		return nil, 0, false
	}
	return cv.result, cv.price, true
}

func send402Gin(c *gin.Context, serverIdentityKey string, sats int) {
	c.Header(pay402.HeaderSats, strconv.Itoa(sats))
	c.Header(pay402.HeaderServer, serverIdentityKey)
	c.Header("Access-Control-Allow-Origin", "*")
	c.Header("Access-Control-Expose-Headers", pay402.HeaderSats+","+pay402.HeaderServer)
	c.AbortWithStatus(http.StatusPaymentRequired)
}
