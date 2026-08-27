package pay402chi

import (
	"context"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	pay402 "github.com/bsv-blockchain/go-402-pay"
	"github.com/bsv-blockchain/go-sdk/chainhash"
	"github.com/bsv-blockchain/go-sdk/script"
	"github.com/bsv-blockchain/go-sdk/transaction"
	"github.com/bsv-blockchain/go-sdk/wallet"
	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

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
	tx.AddOutput(&transaction.TransactionOutput{
		Satoshis:      satoshis,
		LockingScript: script.NewFromBytes([]byte{0x51}),
	})
	beef := transaction.NewBeefV2()
	_, err = beef.MergeRawTx(tx.Bytes(), nil)
	require.NoError(t, err)
	beef.NewestTxID = tx.TxID()
	beefBytes, err := beef.Bytes()
	require.NoError(t, err)
	return base64.StdEncoding.EncodeToString(beefBytes), tx.TxID().String()
}

func makeAcceptingWallet(t *testing.T) *wallet.TestWallet {
	t.Helper()
	w := wallet.NewTestWalletForRandomKey(t)
	w.OnInternalizeAction().ReturnSuccess(&wallet.InternalizeActionResult{Accepted: true})
	return w
}

func makeRejectingWallet(t *testing.T) *wallet.TestWallet {
	t.Helper()
	w := wallet.NewTestWalletForRandomKey(t)
	w.OnInternalizeAction().ReturnSuccess(&wallet.InternalizeActionResult{Accepted: false})
	return w
}

func validPaymentHeaders(beefB64 string) map[string]string {
	return map[string]string{
		pay402.HeaderSender: "03f8104e2b313136ef1b84fcd9c8aadb775beb89a8207c942b31ab89e160ba4c86",
		pay402.HeaderBeef:   beefB64,
		pay402.HeaderNonce:  base64.StdEncoding.EncodeToString([]byte("nonce123")),
		pay402.HeaderTime:   strconv.FormatInt(time.Now().UnixMilli(), 10),
		pay402.HeaderVout:   "0",
	}
}

func newChiWithMiddleware(w wallet.Interface, price int) *chi.Mux {
	r := chi.NewRouter()
	r.Use(PaymentMiddleware(Options{
		Wallet:         w,
		CalculatePrice: func(string) int { return price },
	}))
	r.Get("/paid", func(w http.ResponseWriter, req *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("paid content"))
	})
	r.Get("/free", func(w http.ResponseWriter, req *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("free content"))
	})
	return r
}

func performRequest(r http.Handler, path string, headers map[string]string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, path, nil)
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)
	return rr
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

func TestChi_FreeContent_PassesThrough(t *testing.T) {
	r := newChiWithMiddleware(wallet.NewTestWalletForRandomKey(t), 0)
	rr := performRequest(r, "/free", nil)
	assert.Equal(t, http.StatusOK, rr.Code)
	assert.Equal(t, "free content", rr.Body.String())
}

func TestChi_MissingBeef_Returns402(t *testing.T) {
	r := newChiWithMiddleware(wallet.NewTestWalletForRandomKey(t), 100)
	rr := performRequest(r, "/paid", nil)
	assert.Equal(t, http.StatusPaymentRequired, rr.Code)
}

func TestChi_MissingBeef_SetsSatsHeader(t *testing.T) {
	r := newChiWithMiddleware(wallet.NewTestWalletForRandomKey(t), 250)
	rr := performRequest(r, "/paid", nil)
	assert.Equal(t, "250", rr.Header().Get(pay402.HeaderSats))
}

func TestChi_MissingBeef_SetsServerHeader(t *testing.T) {
	r := newChiWithMiddleware(wallet.NewTestWalletForRandomKey(t), 100)
	rr := performRequest(r, "/paid", nil)
	assert.NotEmpty(t, rr.Header().Get(pay402.HeaderServer))
}

func TestChi_ValidPayment_Returns200(t *testing.T) {
	r := newChiWithMiddleware(makeAcceptingWallet(t), 100)
	beefB64, _ := makeBEEF(t, 100)
	rr := performRequest(r, "/paid", validPaymentHeaders(beefB64))
	assert.Equal(t, http.StatusOK, rr.Code)
	assert.Equal(t, "paid content", rr.Body.String())
}

func TestChi_ValidPayment_StoresInContext(t *testing.T) {
	w := makeAcceptingWallet(t)
	beefB64, _ := makeBEEF(t, 100)

	var gotResult *pay402.PaymentResult
	var gotPrice int
	r := chi.NewRouter()
	r.Use(PaymentMiddleware(Options{
		Wallet:         w,
		CalculatePrice: func(string) int { return 100 },
	}))
	r.Get("/paid", func(w http.ResponseWriter, req *http.Request) {
		gotResult, gotPrice, _ = PaymentFromContext(req.Context())
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("ok"))
	})

	performRequest(r, "/paid", validPaymentHeaders(beefB64))
	require.NotNil(t, gotResult)
	assert.Equal(t, 100, gotPrice)
}

func TestChi_ReplayAttack_Returns402(t *testing.T) {
	r := newChiWithMiddleware(makeRejectingWallet(t), 100)
	beefB64, _ := makeBEEF(t, 100)
	rr := performRequest(r, "/paid", validPaymentHeaders(beefB64))
	assert.Equal(t, http.StatusPaymentRequired, rr.Code)
}

func TestChi_Underpayment_Returns402(t *testing.T) {
	r := newChiWithMiddleware(makeAcceptingWallet(t), 100)
	beefB64, _ := makeBEEF(t, 50)
	rr := performRequest(r, "/paid", validPaymentHeaders(beefB64))
	assert.Equal(t, http.StatusPaymentRequired, rr.Code)
}

func TestChi_WalletError_Returns500(t *testing.T) {
	w := wallet.NewTestWalletForRandomKey(t)
	w.OnGetPublicKey().ReturnError(assert.AnError)
	r := newChiWithMiddleware(w, 100)
	rr := performRequest(r, "/paid", nil)
	assert.Equal(t, http.StatusInternalServerError, rr.Code)
}

func TestChi_PaymentFromContext_MissingReturnsZeroValue(t *testing.T) {
	r := chi.NewRouter()
	r.Get("/test", func(w http.ResponseWriter, req *http.Request) {
		result, price, ok := PaymentFromContext(req.Context())
		assert.False(t, ok)
		assert.Nil(t, result)
		assert.Equal(t, 0, price)
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("ok"))
	})
	performRequest(r, "/test", nil)
}

func TestChi_PaymentFromContext_NilContext_Safe(t *testing.T) {
	// Although req.Context() is never nil in standard library, 
	// a directly provided context could be nil though it shouldn't be.
	result, price, ok := PaymentFromContext(context.Background())
	assert.False(t, ok)
	assert.Nil(t, result)
	assert.Equal(t, 0, price)
}
