// Package pay402fiber provides BRC-121 payment middleware for the Fiber web framework.
//
// Usage:
//
//	import pay402fiber "github.com/bsv-blockchain/go-402-pay/fiber"
//
//	app.Use(pay402fiber.PaymentMiddleware(pay402fiber.Options{
//	    Wallet:         myWallet,
//	    CalculatePrice: func(path string) int { return 100 },
//	}))
//
//	app.Get("/paid", func(c *fiber.Ctx) error {
//	    result, price, ok := pay402fiber.PaymentFromContext(c)
//	    // ...
//	})
package pay402fiber

import (
	"context"
	"log/slog"
	"strconv"

	pay402 "github.com/bsv-blockchain/go-402-pay"
	"github.com/bsv-blockchain/go-sdk/wallet"
	"github.com/gofiber/fiber/v2"
)

// paymentLocalsKey is the key used to store the payment value in fiber.Ctx.Locals.
const paymentLocalsKey = "pay402_payment"

type localsValue struct {
	result *pay402.PaymentResult
	price  int
}

// Options configures the Fiber payment middleware.
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

// PaymentMiddleware returns a Fiber handler that enforces BRC-121 payment on
// requests where CalculatePrice returns a non-zero value.
func PaymentMiddleware(opts Options) fiber.Handler {
	log := opts.Logger
	if log == nil {
		log = slog.Default()
	}
	windowMs := opts.PaymentWindowMs
	if windowMs <= 0 {
		windowMs = pay402.DefaultPaymentWindowMs
	}

	var identityKey string

	return func(c *fiber.Ctx) error {
		ctx := context.Background()

		// Lazy identity key fetch
		if identityKey == "" {
			res, err := opts.Wallet.GetPublicKey(ctx, wallet.GetPublicKeyArgs{IdentityKey: true}, "")
			if err != nil {
				log.ErrorContext(ctx, "Failed to get server identity key", "err", err)
				return c.Status(fiber.StatusInternalServerError).SendString("internal server error")
			}
			identityKey = res.PublicKey.ToDERHex()
		}

		price := opts.CalculatePrice(c.Path())
		if price == 0 {
			return c.Next()
		}

		if c.Get(pay402.HeaderBeef) == "" {
			send402Fiber(c, identityKey, price)
			return nil
		}

		headers := pay402.PaymentHeaders{
			Sender: c.Get(pay402.HeaderSender),
			Beef:   c.Get(pay402.HeaderBeef),
			Nonce:  c.Get(pay402.HeaderNonce),
			Time:   c.Get(pay402.HeaderTime),
			Vout:   c.Get(pay402.HeaderVout),
		}

		result, err := pay402.ValidatePaymentFromHeaders(ctx, headers, c.Path(), opts.Wallet, price, windowMs)
		if err != nil {
			log.ErrorContext(ctx, "Payment rejected", "path", c.Path(), "reason", err.Error())
			send402Fiber(c, identityKey, price)
			return nil
		}
		if result == nil {
			send402Fiber(c, identityKey, price)
			return nil
		}

		log.InfoContext(ctx, "Payment accepted",
			"path", c.Path(),
			"sats", price,
			"txid", result.TXID,
		)

		c.Locals(paymentLocalsKey, &localsValue{result: result, price: price})
		return c.Next()
	}
}

// PaymentFromContext retrieves the payment result stored by PaymentMiddleware.
// Returns (result, price, true) on success, or (nil, 0, false) if not present.
func PaymentFromContext(c *fiber.Ctx) (*pay402.PaymentResult, int, bool) {
	v, ok := c.Locals(paymentLocalsKey).(*localsValue)
	if !ok || v == nil {
		return nil, 0, false
	}
	return v.result, v.price, true
}

func send402Fiber(c *fiber.Ctx, serverIdentityKey string, sats int) {
	c.Set(pay402.HeaderSats, strconv.Itoa(sats))
	c.Set(pay402.HeaderServer, serverIdentityKey)
	c.Set("Access-Control-Allow-Origin", "*")
	c.Set("Access-Control-Expose-Headers", pay402.HeaderSats+","+pay402.HeaderServer)
	c.Status(fiber.StatusPaymentRequired)
}
