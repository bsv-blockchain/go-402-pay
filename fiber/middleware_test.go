package pay402fiber

import (
	"encoding/base64"
	"io"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	pay402 "github.com/bsv-blockchain/go-402-pay"
	"github.com/bsv-blockchain/go-sdk/chainhash"
	"github.com/bsv-blockchain/go-sdk/script"
	"github.com/bsv-blockchain/go-sdk/transaction"
	"github.com/bsv-blockchain/go-sdk/wallet"
	"github.com/gofiber/fiber/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ---------------------------------------------------------------------------
// Helpers (mirrors server_test.go helpers, independent of the root package tests)
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

// testFiberApp creates a Fiber app with the payment middleware and a simple paid handler.
func testFiberApp(t *testing.T, w wallet.Interface, price int) *fiber.App {
	t.Helper()
	app := fiber.New()
	app.Use(PaymentMiddleware(Options{
		Wallet:         w,
		CalculatePrice: func(string) int { return price },
	}))
	app.Get("/paid", func(c *fiber.Ctx) error {
		return c.SendString("paid content")
	})
	app.Get("/free", func(c *fiber.Ctx) error {
		return c.SendString("free content")
	})
	return app
}

func doFiberRequest(t *testing.T, app *fiber.App, path string, headers map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest("GET", path, nil)
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := app.Test(req, -1)
	require.NoError(t, err)
	defer resp.Body.Close()

	rr := httptest.NewRecorder()
	rr.Code = resp.StatusCode
	for k, vals := range resp.Header {
		for _, v := range vals {
			rr.Header().Set(k, v)
		}
	}
	body, _ := io.ReadAll(resp.Body)
	rr.Body.WriteString(string(body))
	return rr
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

func TestFiber_FreeContent_PassesThrough(t *testing.T) {
	app := testFiberApp(t, wallet.NewTestWalletForRandomKey(t), 0)
	rr := doFiberRequest(t, app, "/free", nil)
	assert.Equal(t, 200, rr.Code)
	assert.Equal(t, "free content", rr.Body.String())
}

func TestFiber_MissingBeef_Returns402(t *testing.T) {
	app := testFiberApp(t, wallet.NewTestWalletForRandomKey(t), 100)
	rr := doFiberRequest(t, app, "/paid", nil)
	assert.Equal(t, 402, rr.Code)
}

func TestFiber_MissingBeef_SetsSatsHeader(t *testing.T) {
	app := testFiberApp(t, wallet.NewTestWalletForRandomKey(t), 250)
	rr := doFiberRequest(t, app, "/paid", nil)
	assert.Equal(t, "250", rr.Header().Get(pay402.HeaderSats))
}

func TestFiber_MissingBeef_SetsServerHeader(t *testing.T) {
	app := testFiberApp(t, wallet.NewTestWalletForRandomKey(t), 100)
	rr := doFiberRequest(t, app, "/paid", nil)
	assert.NotEmpty(t, rr.Header().Get(pay402.HeaderServer))
}

func TestFiber_ValidPayment_Returns200(t *testing.T) {
	w := makeAcceptingWallet(t)
	app := testFiberApp(t, w, 100)
	beefB64, _ := makeBEEF(t, 100)
	rr := doFiberRequest(t, app, "/paid", validPaymentHeaders(beefB64))
	assert.Equal(t, 200, rr.Code)
	assert.Equal(t, "paid content", rr.Body.String())
}

func TestFiber_ValidPayment_StoresInLocals(t *testing.T) {
	w := makeAcceptingWallet(t)
	beefB64, _ := makeBEEF(t, 100)

	app := fiber.New()
	app.Use(PaymentMiddleware(Options{
		Wallet:         w,
		CalculatePrice: func(string) int { return 100 },
	}))

	var gotResult *pay402.PaymentResult
	var gotPrice int
	app.Get("/paid", func(c *fiber.Ctx) error {
		gotResult, gotPrice, _ = PaymentFromContext(c)
		return c.SendString("ok")
	})

	req := httptest.NewRequest("GET", "/paid", nil)
	for k, v := range validPaymentHeaders(beefB64) {
		req.Header.Set(k, v)
	}
	app.Test(req, -1) //nolint:errcheck
	require.NotNil(t, gotResult)
	assert.Equal(t, 100, gotPrice)
}

func TestFiber_ReplayAttack_Returns402(t *testing.T) {
	w := makeRejectingWallet(t)
	app := testFiberApp(t, w, 100)
	beefB64, _ := makeBEEF(t, 100)
	rr := doFiberRequest(t, app, "/paid", validPaymentHeaders(beefB64))
	assert.Equal(t, 402, rr.Code)
}

func TestFiber_Underpayment_Returns402(t *testing.T) {
	w := makeAcceptingWallet(t)
	app := testFiberApp(t, w, 100)
	beefB64, _ := makeBEEF(t, 50) // only 50 sats, need 100
	rr := doFiberRequest(t, app, "/paid", validPaymentHeaders(beefB64))
	assert.Equal(t, 402, rr.Code)
}

func TestFiber_PaymentFromContext_MissingReturnsZeroValue(t *testing.T) {
	app := fiber.New()
	app.Get("/test", func(c *fiber.Ctx) error {
		result, price, ok := PaymentFromContext(c)
		assert.False(t, ok)
		assert.Nil(t, result)
		assert.Equal(t, 0, price)
		return c.SendString("ok")
	})
	req := httptest.NewRequest("GET", "/test", nil)
	app.Test(req, -1) //nolint:errcheck
}
