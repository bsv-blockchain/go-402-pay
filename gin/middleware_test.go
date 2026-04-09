package pay402gin

import (
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
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func init() {
	gin.SetMode(gin.TestMode)
}

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

func newGinWithMiddleware(w wallet.Interface, price int) *gin.Engine {
	r := gin.New()
	r.Use(PaymentMiddleware(Options{
		Wallet:         w,
		CalculatePrice: func(string) int { return price },
	}))
	r.GET("/paid", func(c *gin.Context) {
		c.String(http.StatusOK, "paid content")
	})
	r.GET("/free", func(c *gin.Context) {
		c.String(http.StatusOK, "free content")
	})
	return r
}

func performRequest(r *gin.Engine, path string, headers map[string]string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, path, nil)
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

func TestGin_FreeContent_PassesThrough(t *testing.T) {
	r := newGinWithMiddleware(wallet.NewTestWalletForRandomKey(t), 0)
	w := performRequest(r, "/free", nil)
	assert.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, "free content", w.Body.String())
}

func TestGin_MissingBeef_Returns402(t *testing.T) {
	r := newGinWithMiddleware(wallet.NewTestWalletForRandomKey(t), 100)
	w := performRequest(r, "/paid", nil)
	assert.Equal(t, http.StatusPaymentRequired, w.Code)
}

func TestGin_MissingBeef_SetsSatsHeader(t *testing.T) {
	r := newGinWithMiddleware(wallet.NewTestWalletForRandomKey(t), 250)
	w := performRequest(r, "/paid", nil)
	assert.Equal(t, "250", w.Header().Get(pay402.HeaderSats))
}

func TestGin_MissingBeef_SetsServerHeader(t *testing.T) {
	r := newGinWithMiddleware(wallet.NewTestWalletForRandomKey(t), 100)
	w := performRequest(r, "/paid", nil)
	assert.NotEmpty(t, w.Header().Get(pay402.HeaderServer))
}

func TestGin_ValidPayment_Returns200(t *testing.T) {
	r := newGinWithMiddleware(makeAcceptingWallet(t), 100)
	beefB64, _ := makeBEEF(t, 100)
	w := performRequest(r, "/paid", validPaymentHeaders(beefB64))
	assert.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, "paid content", w.Body.String())
}

func TestGin_ValidPayment_StoresInContext(t *testing.T) {
	w := makeAcceptingWallet(t)
	beefB64, _ := makeBEEF(t, 100)

	var gotResult *pay402.PaymentResult
	var gotPrice int
	r := gin.New()
	r.Use(PaymentMiddleware(Options{
		Wallet:         w,
		CalculatePrice: func(string) int { return 100 },
	}))
	r.GET("/paid", func(c *gin.Context) {
		gotResult, gotPrice, _ = PaymentFromContext(c)
		c.String(http.StatusOK, "ok")
	})

	performRequest(r, "/paid", validPaymentHeaders(beefB64))
	require.NotNil(t, gotResult)
	assert.Equal(t, 100, gotPrice)
}

func TestGin_ReplayAttack_Returns402(t *testing.T) {
	r := newGinWithMiddleware(makeRejectingWallet(t), 100)
	beefB64, _ := makeBEEF(t, 100)
	rr := performRequest(r, "/paid", validPaymentHeaders(beefB64))
	assert.Equal(t, http.StatusPaymentRequired, rr.Code)
}

func TestGin_Underpayment_Returns402(t *testing.T) {
	r := newGinWithMiddleware(makeAcceptingWallet(t), 100)
	beefB64, _ := makeBEEF(t, 50)
	rr := performRequest(r, "/paid", validPaymentHeaders(beefB64))
	assert.Equal(t, http.StatusPaymentRequired, rr.Code)
}

func TestGin_WalletError_Returns500(t *testing.T) {
	w := wallet.NewTestWalletForRandomKey(t)
	w.OnGetPublicKey().ReturnError(assert.AnError)
	r := newGinWithMiddleware(w, 100)
	rr := performRequest(r, "/paid", nil)
	assert.Equal(t, http.StatusInternalServerError, rr.Code)
}

func TestGin_PaymentFromContext_MissingReturnsZeroValue(t *testing.T) {
	r := gin.New()
	r.GET("/test", func(c *gin.Context) {
		result, price, ok := PaymentFromContext(c)
		assert.False(t, ok)
		assert.Nil(t, result)
		assert.Equal(t, 0, price)
		c.String(http.StatusOK, "ok")
	})
	performRequest(r, "/test", nil)
}
