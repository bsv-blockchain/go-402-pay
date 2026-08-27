// Package pay402echo provides BRC-121 payment middleware for the Echo web framework.
//
// Usage:
//
//	import pay402echo "github.com/bsv-blockchain/go-402-pay/echo"
//
//	e.Use(pay402echo.PaymentMiddleware(pay402echo.Options{
//	    Wallet:         myWallet,
//	    CalculatePrice: func(path string) int { return 100 },
//	}))
//
//	e.GET("/paid", func(c echo.Context) error {
//	    result, price, ok := pay402echo.PaymentFromContext(c)
//	    // ...
//	    return nil
//	})
package pay402echo

import (
	"context"
	"log/slog"
	"net/http"
	"strconv"

	pay402 "github.com/bsv-blockchain/go-402-pay"
	"github.com/bsv-blockchain/go-sdk/wallet"
	"github.com/labstack/echo/v4"
)

// paymentContextKey is the key used to store the payment value in echo.Context.
const paymentContextKey = "pay402_payment"

type contextValue struct {
	result *pay402.PaymentResult
	price  int
}

// Options configures the Echo payment middleware.
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

// PaymentMiddleware returns an Echo MiddlewareFunc that enforces BRC-121 payment
// on requests where CalculatePrice returns a non-zero value.
func PaymentMiddleware(opts Options) echo.MiddlewareFunc {
	log := opts.Logger
	if log == nil {
		log = slog.Default()
	}
	windowMs := opts.PaymentWindowMs
	if windowMs <= 0 {
		windowMs = pay402.DefaultPaymentWindowMs
	}

	var identityKey string

	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			ctx := context.Background()

			// Lazy identity key fetch
			if identityKey == "" {
				res, err := opts.Wallet.GetPublicKey(ctx, wallet.GetPublicKeyArgs{IdentityKey: true}, "")
				if err != nil {
					log.ErrorContext(ctx, "Failed to get server identity key", "err", err)
					return c.NoContent(http.StatusInternalServerError)
				}
				identityKey = res.PublicKey.ToDERHex()
			}

			price := opts.CalculatePrice(c.Request().URL.Path)
			if price == 0 {
				return next(c)
			}

			if c.Request().Header.Get(pay402.HeaderBeef) == "" {
				send402Echo(c, identityKey, price)
				return nil
			}

			headers := pay402.PaymentHeaders{
				Sender: c.Request().Header.Get(pay402.HeaderSender),
				Beef:   c.Request().Header.Get(pay402.HeaderBeef),
				Nonce:  c.Request().Header.Get(pay402.HeaderNonce),
				Time:   c.Request().Header.Get(pay402.HeaderTime),
				Vout:   c.Request().Header.Get(pay402.HeaderVout),
			}

			result, err := pay402.ValidatePaymentFromHeaders(ctx, headers, c.Request().URL.Path, opts.Wallet, price, windowMs)
			if err != nil {
				log.ErrorContext(ctx, "Payment rejected", "path", c.Request().URL.Path, "reason", err.Error())
				send402Echo(c, identityKey, price)
				return nil
			}
			if result == nil {
				send402Echo(c, identityKey, price)
				return nil
			}

			log.InfoContext(ctx, "Payment accepted",
				"path", c.Request().URL.Path,
				"sats", price,
				"txid", result.TXID,
			)

			c.Set(paymentContextKey, &contextValue{result: result, price: price})
			return next(c)
		}
	}
}

// PaymentFromContext retrieves the payment result stored by PaymentMiddleware.
// Returns (result, price, true) on success, or (nil, 0, false) if not present.
func PaymentFromContext(c echo.Context) (*pay402.PaymentResult, int, bool) {
	v := c.Get(paymentContextKey)
	if v == nil {
		return nil, 0, false
	}
	cv, ok := v.(*contextValue)
	if !ok || cv == nil {
		return nil, 0, false
	}
	return cv.result, cv.price, true
}

func send402Echo(c echo.Context, serverIdentityKey string, sats int) {
	c.Response().Header().Set(pay402.HeaderSats, strconv.Itoa(sats))
	c.Response().Header().Set(pay402.HeaderServer, serverIdentityKey)
	c.Response().Header().Set("Access-Control-Allow-Origin", "*")
	c.Response().Header().Set("Access-Control-Expose-Headers", pay402.HeaderSats+","+pay402.HeaderServer)
	c.Response().WriteHeader(http.StatusPaymentRequired)
}
