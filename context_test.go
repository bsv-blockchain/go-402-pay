package pay402

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestPaymentFromContext_Missing(t *testing.T) {
	result, price, ok := PaymentFromContext(context.Background())
	assert.False(t, ok)
	assert.Nil(t, result)
	assert.Equal(t, 0, price)
}

func TestPaymentFromContext_RoundTrip(t *testing.T) {
	want := &PaymentResult{
		SatoshisPaid:      100,
		SenderIdentityKey: "03abc",
		TXID:              "deadbeef",
	}
	ctx := contextWithPayment(context.Background(), want, 100)

	got, price, ok := PaymentFromContext(ctx)
	assert.True(t, ok)
	assert.Equal(t, want, got)
	assert.Equal(t, 100, price)
}

func TestPaymentFromContext_DoesNotLeakToParent(t *testing.T) {
	parent := context.Background()
	_ = contextWithPayment(parent, &PaymentResult{TXID: "abc"}, 50)

	_, _, ok := PaymentFromContext(parent)
	assert.False(t, ok, "parent context should not be affected")
}

func TestPaymentFromContext_ChildInheritsFromParent(t *testing.T) {
	result := &PaymentResult{TXID: "abc", SatoshisPaid: 50}
	ctx := contextWithPayment(context.Background(), result, 50)
	child := context.WithValue(ctx, struct{ k string }{"other"}, "val")

	got, price, ok := PaymentFromContext(child)
	assert.True(t, ok)
	assert.Equal(t, result, got)
	assert.Equal(t, 50, price)
}
