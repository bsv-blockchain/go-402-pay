package pay402

import "context"

// paymentContextKey is an unexported type to prevent collisions with other packages.
type paymentContextKey struct{}

// paymentContextValue bundles the PaymentResult with the price charged by the endpoint.
type paymentContextValue struct {
	result *PaymentResult
	price  int
}

// contextWithPayment returns a new context carrying the payment result and the price.
func contextWithPayment(ctx context.Context, result *PaymentResult, price int) context.Context {
	return context.WithValue(ctx, paymentContextKey{}, &paymentContextValue{
		result: result,
		price:  price,
	})
}

// PaymentFromContext retrieves the payment result from the context.
// Returns the result, the price charged, and true if a payment was found.
// Returns nil, 0, false if no payment is present (e.g. free endpoint).
func PaymentFromContext(ctx context.Context) (*PaymentResult, int, bool) {
	v, ok := ctx.Value(paymentContextKey{}).(*paymentContextValue)
	if !ok || v == nil {
		return nil, 0, false
	}
	return v.result, v.price, true
}
