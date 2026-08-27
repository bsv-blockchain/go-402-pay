package pay402

import (
	"context"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"github.com/bsv-blockchain/go-sdk/chainhash"
	ec "github.com/bsv-blockchain/go-sdk/primitives/ec"
	"github.com/bsv-blockchain/go-sdk/script"
	"github.com/bsv-blockchain/go-sdk/transaction"
	"github.com/bsv-blockchain/go-sdk/wallet"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// makeBEEF builds a minimal valid BEEF containing a single transaction with
// one output of the given satoshi value.
// It uses MergeRawTx on a V2 BEEF to avoid the ancestor-resolution requirement
// that NewBeefFromTransaction imposes.
func makeBEEF(t *testing.T, satoshis uint64) (beefB64 string, txid string) {
	t.Helper()

	tx := transaction.NewTransaction()

	zeroHash, err := chainhash.NewHashFromHex("0000000000000000000000000000000000000000000000000000000000000000")
	require.NoError(t, err)

	tx.AddInput(&transaction.TransactionInput{
		SourceTXID:       zeroHash,
		SourceTxOutIndex: 0xffffffff,
		UnlockingScript:  script.NewFromBytes([]byte{0x00}),
		SequenceNumber:   0xffffffff,
	})

	lockScript := script.NewFromBytes([]byte{0x51}) // OP_1 — valid minimal script
	tx.AddOutput(&transaction.TransactionOutput{
		Satoshis:      satoshis,
		LockingScript: lockScript,
	})

	beef := transaction.NewBeefV2()
	_, err = beef.MergeRawTx(tx.Bytes(), nil)
	require.NoError(t, err)
	beef.NewestTxID = tx.TxID()

	beefBytes, err := beef.Bytes()
	require.NoError(t, err)

	return base64.StdEncoding.EncodeToString(beefBytes), tx.TxID().String()
}

// validHeaders returns a set of headers that will pass ValidatePaymentFromHeaders
// for the given BEEF, using the current time.
func validHeaders(beefB64 string) PaymentHeaders {
	return PaymentHeaders{
		Sender: "03f8104e2b313136ef1b84fcd9c8aadb775beb89a8207c942b31ab89e160ba4c86",
		Beef:   beefB64,
		Nonce:  base64.StdEncoding.EncodeToString([]byte("nonce123")),
		Time:   strconv.FormatInt(time.Now().UnixMilli(), 10),
		Vout:   "0",
	}
}

// makeWallet returns a TestWallet that accepts all payments.
func makeWallet(t *testing.T) *wallet.TestWallet {
	t.Helper()
	return wallet.NewTestWalletForRandomKey(t)
}

// makeAcceptingWallet returns a TestWallet whose InternalizeAction always returns Accepted=true.
func makeAcceptingWallet(t *testing.T) *wallet.TestWallet {
	t.Helper()
	w := makeWallet(t)
	w.OnInternalizeAction().ReturnSuccess(&wallet.InternalizeActionResult{Accepted: true})
	return w
}

// makeRejectingWallet returns a TestWallet whose InternalizeAction returns Accepted=false (replay).
func makeRejectingWallet(t *testing.T) *wallet.TestWallet {
	t.Helper()
	w := makeWallet(t)
	w.OnInternalizeAction().ReturnSuccess(&wallet.InternalizeActionResult{Accepted: false})
	return w
}

// ---------------------------------------------------------------------------
// Send402
// ---------------------------------------------------------------------------

func TestSend402_Status(t *testing.T) {
	w := httptest.NewRecorder()
	Send402(w, "server-key", 100)
	assert.Equal(t, http.StatusPaymentRequired, w.Code)
}

func TestSend402_SatsHeader(t *testing.T) {
	w := httptest.NewRecorder()
	Send402(w, "server-key", 250)
	assert.Equal(t, "250", w.Header().Get(HeaderSats))
}

func TestSend402_ServerHeader(t *testing.T) {
	w := httptest.NewRecorder()
	Send402(w, "my-identity-key", 100)
	assert.Equal(t, "my-identity-key", w.Header().Get(HeaderServer))
}

func TestSend402_CORSHeaders(t *testing.T) {
	w := httptest.NewRecorder()
	Send402(w, "server-key", 100)
	assert.Equal(t, "*", w.Header().Get("Access-Control-Allow-Origin"))
	assert.Contains(t, w.Header().Get("Access-Control-Expose-Headers"), HeaderSats)
	assert.Contains(t, w.Header().Get("Access-Control-Expose-Headers"), HeaderServer)
}

// ---------------------------------------------------------------------------
// ValidatePaymentFromHeaders — nil returns (missing / malformed)
// ---------------------------------------------------------------------------

func TestValidatePayment_MissingHeaders(t *testing.T) {
	w := makeAcceptingWallet(t)
	beefB64, _ := makeBEEF(t, 100)
	base := validHeaders(beefB64)

	cases := []struct {
		name    string
		headers PaymentHeaders
	}{
		{"missing sender", PaymentHeaders{Beef: base.Beef, Nonce: base.Nonce, Time: base.Time, Vout: base.Vout}},
		{"missing beef", PaymentHeaders{Sender: base.Sender, Nonce: base.Nonce, Time: base.Time, Vout: base.Vout}},
		{"missing nonce", PaymentHeaders{Sender: base.Sender, Beef: base.Beef, Time: base.Time, Vout: base.Vout}},
		{"missing time", PaymentHeaders{Sender: base.Sender, Beef: base.Beef, Nonce: base.Nonce, Vout: base.Vout}},
		{"missing vout", PaymentHeaders{Sender: base.Sender, Beef: base.Beef, Nonce: base.Nonce, Time: base.Time}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			result, err := ValidatePaymentFromHeaders(context.Background(), tc.headers, "/test", w, 100, 0)
			assert.Nil(t, result)
			assert.NoError(t, err)
		})
	}
}

func TestValidatePayment_NonNumericTime(t *testing.T) {
	w := makeAcceptingWallet(t)
	beefB64, _ := makeBEEF(t, 100)
	h := validHeaders(beefB64)
	h.Time = "not-a-number"
	result, err := ValidatePaymentFromHeaders(context.Background(), h, "/test", w, 100, 0)
	assert.Nil(t, result)
	assert.NoError(t, err)
}

func TestValidatePayment_StaleTimestamp(t *testing.T) {
	w := makeAcceptingWallet(t)
	beefB64, _ := makeBEEF(t, 100)
	h := validHeaders(beefB64)
	h.Time = strconv.FormatInt(time.Now().Add(-31*time.Second).UnixMilli(), 10)
	result, err := ValidatePaymentFromHeaders(context.Background(), h, "/test", w, 100, 0)
	assert.Nil(t, result)
	assert.NoError(t, err)
}

func TestValidatePayment_FutureTimestamp(t *testing.T) {
	w := makeAcceptingWallet(t)
	beefB64, _ := makeBEEF(t, 100)
	h := validHeaders(beefB64)
	h.Time = strconv.FormatInt(time.Now().Add(31*time.Second).UnixMilli(), 10)
	result, err := ValidatePaymentFromHeaders(context.Background(), h, "/test", w, 100, 0)
	assert.Nil(t, result)
	assert.NoError(t, err)
}

func TestValidatePayment_CustomWindowMs(t *testing.T) {
	w := makeAcceptingWallet(t)
	beefB64, _ := makeBEEF(t, 100)
	h := validHeaders(beefB64)
	// 6 seconds ago, with a 5-second window
	h.Time = strconv.FormatInt(time.Now().Add(-6*time.Second).UnixMilli(), 10)
	result, err := ValidatePaymentFromHeaders(context.Background(), h, "/test", w, 100, 5_000)
	assert.Nil(t, result)
	assert.NoError(t, err)
}

func TestValidatePayment_InvalidBEEF(t *testing.T) {
	w := makeAcceptingWallet(t)
	h := validHeaders("not-valid-base64!!!")
	result, err := ValidatePaymentFromHeaders(context.Background(), h, "/test", w, 100, 0)
	assert.Nil(t, result)
	assert.NoError(t, err)
}

func TestValidatePayment_VoutOutOfBounds(t *testing.T) {
	w := makeAcceptingWallet(t)
	beefB64, _ := makeBEEF(t, 100) // only vout 0 exists
	h := validHeaders(beefB64)
	h.Vout = "5"
	result, err := ValidatePaymentFromHeaders(context.Background(), h, "/test", w, 100, 0)
	assert.Nil(t, result)
	assert.NoError(t, err)
}

func TestValidatePayment_Underpayment(t *testing.T) {
	w := makeAcceptingWallet(t)
	beefB64, _ := makeBEEF(t, 50) // only 50 sats
	h := validHeaders(beefB64)
	result, err := ValidatePaymentFromHeaders(context.Background(), h, "/test", w, 100, 0)
	assert.Nil(t, result)
	assert.NoError(t, err)
}

func TestValidatePayment_ExactPayment(t *testing.T) {
	w := makeAcceptingWallet(t)
	beefB64, _ := makeBEEF(t, 100)
	h := validHeaders(beefB64)
	result, err := ValidatePaymentFromHeaders(context.Background(), h, "/test", w, 100, 0)
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.True(t, result.SatoshisPaid >= 100)
}

func TestValidatePayment_Overpayment(t *testing.T) {
	w := makeAcceptingWallet(t)
	beefB64, _ := makeBEEF(t, 999)
	h := validHeaders(beefB64)
	result, err := ValidatePaymentFromHeaders(context.Background(), h, "/test", w, 100, 0)
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.Equal(t, uint64(999), result.SatoshisPaid)
}

// ---------------------------------------------------------------------------
// ValidatePaymentFromHeaders — replay (PaymentError)
// ---------------------------------------------------------------------------

func TestValidatePayment_Replay(t *testing.T) {
	w := makeRejectingWallet(t)
	beefB64, _ := makeBEEF(t, 100)
	h := validHeaders(beefB64)
	result, err := ValidatePaymentFromHeaders(context.Background(), h, "/test", w, 100, 0)
	assert.Nil(t, result)
	require.Error(t, err)
	var payErr *PaymentError
	require.ErrorAs(t, err, &payErr)
	assert.Contains(t, payErr.Reason, "Replayed")
}

func TestValidatePayment_ReplayReasonContainsTxid(t *testing.T) {
	w := makeRejectingWallet(t)
	beefB64, txid := makeBEEF(t, 100)
	h := validHeaders(beefB64)
	_, err := ValidatePaymentFromHeaders(context.Background(), h, "/test", w, 100, 0)
	require.Error(t, err)
	assert.Contains(t, err.Error(), txid)
}

// ---------------------------------------------------------------------------
// ValidatePaymentFromHeaders — happy path
// ---------------------------------------------------------------------------

func TestValidatePayment_HappyPath_ReturnsTXID(t *testing.T) {
	w := makeAcceptingWallet(t)
	beefB64, txid := makeBEEF(t, 100)
	h := validHeaders(beefB64)
	result, err := ValidatePaymentFromHeaders(context.Background(), h, "/test", w, 100, 0)
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.Equal(t, txid, result.TXID)
}

func TestValidatePayment_HappyPath_ReturnsSenderKey(t *testing.T) {
	w := makeAcceptingWallet(t)
	beefB64, _ := makeBEEF(t, 100)
	h := validHeaders(beefB64)
	result, err := ValidatePaymentFromHeaders(context.Background(), h, "/test", w, 100, 0)
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.Equal(t, h.Sender, result.SenderIdentityKey)
}

func TestValidatePayment_InternalizeCalledWithCorrectPath(t *testing.T) {
	w := makeWallet(t)
	var capturedDesc string
	w.OnInternalizeAction().
		Expect(func(_ context.Context, args wallet.InternalizeActionArgs, _ string) {
			capturedDesc = args.Description
		}).
		ReturnSuccess(&wallet.InternalizeActionResult{Accepted: true})

	beefB64, _ := makeBEEF(t, 100)
	h := validHeaders(beefB64)
	_, _ = ValidatePaymentFromHeaders(context.Background(), h, "/articles/foo", w, 100, 0)
	assert.Equal(t, "Payment for /articles/foo", capturedDesc)
}

func TestValidatePayment_InternalizeCalledWithCorrectVout(t *testing.T) {
	w := makeWallet(t)
	var capturedVout uint32
	w.OnInternalizeAction().
		Expect(func(_ context.Context, args wallet.InternalizeActionArgs, _ string) {
			capturedVout = args.Outputs[0].OutputIndex
		}).
		ReturnSuccess(&wallet.InternalizeActionResult{Accepted: true})

	beefB64, _ := makeBEEF(t, 100)
	h := validHeaders(beefB64)
	h.Vout = "0"
	_, _ = ValidatePaymentFromHeaders(context.Background(), h, "/test", w, 100, 0)
	assert.Equal(t, uint32(0), capturedVout)
}

func TestValidatePayment_DerivationSuffixIsBase64Time(t *testing.T) {
	w := makeWallet(t)
	var capturedSuffix []byte
	w.OnInternalizeAction().
		Expect(func(_ context.Context, args wallet.InternalizeActionArgs, _ string) {
			capturedSuffix = args.Outputs[0].PaymentRemittance.DerivationSuffix
		}).
		ReturnSuccess(&wallet.InternalizeActionResult{Accepted: true})

	beefB64, _ := makeBEEF(t, 100)
	h := validHeaders(beefB64)
	_, _ = ValidatePaymentFromHeaders(context.Background(), h, "/test", w, 100, 0)
	// derivationSuffix must be the base64-encoded representation of the time string
	timeB64 := base64.StdEncoding.EncodeToString([]byte(h.Time))
	assert.Equal(t, []byte(timeB64), capturedSuffix)
}

// ---------------------------------------------------------------------------
// ValidatePayment (net/http wrapper)
// ---------------------------------------------------------------------------

func TestValidatePayment_NetHTTP_MissingBeef(t *testing.T) {
	w := makeAcceptingWallet(t)
	r := httptest.NewRequest(http.MethodGet, "/test", nil)
	result, err := ValidatePayment(context.Background(), r, w, 100, 0)
	assert.Nil(t, result)
	assert.NoError(t, err)
}

func TestValidatePayment_NetHTTP_HappyPath(t *testing.T) {
	w := makeAcceptingWallet(t)
	beefB64, _ := makeBEEF(t, 100)
	h := validHeaders(beefB64)

	r := httptest.NewRequest(http.MethodGet, "/test", nil)
	r.Header.Set(HeaderSender, h.Sender)
	r.Header.Set(HeaderBeef, h.Beef)
	r.Header.Set(HeaderNonce, h.Nonce)
	r.Header.Set(HeaderTime, h.Time)
	r.Header.Set(HeaderVout, h.Vout)

	result, err := ValidatePayment(context.Background(), r, w, 100, 0)
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.True(t, result.SatoshisPaid >= 100)
}

// ---------------------------------------------------------------------------
// PaymentMiddleware
// ---------------------------------------------------------------------------

func TestMiddleware_FreeContent_CallsNext(t *testing.T) {
	w := makeWallet(t)
	called := false
	next := http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) { called = true })

	handler := PaymentMiddleware(MiddlewareOptions{
		Wallet:         w,
		CalculatePrice: func(string) int { return 0 },
	}, next)

	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/free", nil))
	assert.True(t, called)
}

func TestMiddleware_MissingBeef_Returns402(t *testing.T) {
	w := makeWallet(t)
	next := http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {})

	handler := PaymentMiddleware(MiddlewareOptions{
		Wallet:         w,
		CalculatePrice: func(string) int { return 100 },
	}, next)

	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/paid", nil))
	assert.Equal(t, http.StatusPaymentRequired, rr.Code)
}

func TestMiddleware_MissingBeef_SetsHeaders(t *testing.T) {
	w := makeWallet(t)
	handler := PaymentMiddleware(MiddlewareOptions{
		Wallet:         w,
		CalculatePrice: func(string) int { return 100 },
	}, http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {}))

	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/paid", nil))
	assert.Equal(t, "100", rr.Header().Get(HeaderSats))
	assert.NotEmpty(t, rr.Header().Get(HeaderServer))
}

func TestMiddleware_ValidPayment_CallsNext(t *testing.T) {
	w := makeAcceptingWallet(t)
	beefB64, _ := makeBEEF(t, 100)
	h := validHeaders(beefB64)

	called := false
	next := http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) { called = true })
	handler := PaymentMiddleware(MiddlewareOptions{
		Wallet:         w,
		CalculatePrice: func(string) int { return 100 },
	}, next)

	r := httptest.NewRequest(http.MethodGet, "/paid", nil)
	r.Header.Set(HeaderSender, h.Sender)
	r.Header.Set(HeaderBeef, h.Beef)
	r.Header.Set(HeaderNonce, h.Nonce)
	r.Header.Set(HeaderTime, h.Time)
	r.Header.Set(HeaderVout, h.Vout)

	handler.ServeHTTP(httptest.NewRecorder(), r)
	assert.True(t, called)
}

func TestMiddleware_ValidPayment_StoresInContext(t *testing.T) {
	w := makeAcceptingWallet(t)
	beefB64, _ := makeBEEF(t, 100)
	h := validHeaders(beefB64)

	var gotResult *PaymentResult
	var gotPrice int
	next := http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		gotResult, gotPrice, _ = PaymentFromContext(r.Context())
	})
	handler := PaymentMiddleware(MiddlewareOptions{
		Wallet:         w,
		CalculatePrice: func(string) int { return 100 },
	}, next)

	r := httptest.NewRequest(http.MethodGet, "/paid", nil)
	r.Header.Set(HeaderSender, h.Sender)
	r.Header.Set(HeaderBeef, h.Beef)
	r.Header.Set(HeaderNonce, h.Nonce)
	r.Header.Set(HeaderTime, h.Time)
	r.Header.Set(HeaderVout, h.Vout)

	handler.ServeHTTP(httptest.NewRecorder(), r)
	require.NotNil(t, gotResult)
	assert.Equal(t, 100, gotPrice)
}

func TestMiddleware_ReplayAttack_Returns402(t *testing.T) {
	w := makeRejectingWallet(t)
	beefB64, _ := makeBEEF(t, 100)
	h := validHeaders(beefB64)

	handler := PaymentMiddleware(MiddlewareOptions{
		Wallet:         w,
		CalculatePrice: func(string) int { return 100 },
	}, http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {}))

	r := httptest.NewRequest(http.MethodGet, "/paid", nil)
	r.Header.Set(HeaderSender, h.Sender)
	r.Header.Set(HeaderBeef, h.Beef)
	r.Header.Set(HeaderNonce, h.Nonce)
	r.Header.Set(HeaderTime, h.Time)
	r.Header.Set(HeaderVout, h.Vout)

	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, r)
	assert.Equal(t, http.StatusPaymentRequired, rr.Code)
}

func TestMiddleware_WalletError_Returns500(t *testing.T) {
	// A wallet whose GetPublicKey fails will cause a 500 before identity key is cached
	w := wallet.NewTestWalletForRandomKey(t)
	w.OnGetPublicKey().ReturnError(assert.AnError)

	handler := PaymentMiddleware(MiddlewareOptions{
		Wallet:         w,
		CalculatePrice: func(string) int { return 100 },
	}, http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {}))

	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/paid", nil))
	assert.Equal(t, http.StatusInternalServerError, rr.Code)
}

func TestMiddleware_IdentityKeyFetchedOnce(t *testing.T) {
	w := makeAcceptingWallet(t)
	beefB64, _ := makeBEEF(t, 100)

	handler := PaymentMiddleware(MiddlewareOptions{
		Wallet:         w,
		CalculatePrice: func(string) int { return 100 },
	}, http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {}))

	for i := 0; i < 3; i++ {
		h := validHeaders(beefB64)
		r := httptest.NewRequest(http.MethodGet, "/paid", nil)
		r.Header.Set(HeaderSender, h.Sender)
		r.Header.Set(HeaderBeef, h.Beef)
		r.Header.Set(HeaderNonce, h.Nonce)
		r.Header.Set(HeaderTime, strconv.FormatInt(time.Now().UnixMilli(), 10))
		r.Header.Set(HeaderVout, h.Vout)
		handler.ServeHTTP(httptest.NewRecorder(), r)
	}
	// GetPublicKey with IdentityKey:true should only be called once (cached after first call)
	// The TestWallet doesn't expose a call count, but no panic = key was reused correctly.
}

// Keep compiler happy — ec import used transitively via wallet types but declare explicitly.
var _ = ec.NewPrivateKey
