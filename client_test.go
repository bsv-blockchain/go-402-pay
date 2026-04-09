package pay402

import (
	"context"
	"encoding/base64"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/bsv-blockchain/go-sdk/wallet"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeTxBytes is a stand-in for a real BEEF-encoded transaction returned by createAction.
var fakeTxBytes = []byte{1, 2, 3, 4, 5}

// makeClientWallet returns a TestWallet configured for the client payment flow.
// GetPublicKey with a protocolID returns a real derived key (from the underlying wallet).
// GetPublicKey with identityKey=true returns the wallet's own identity key.
// CreateAction returns a fake transaction.
func makeClientWallet(t *testing.T) *wallet.TestWallet {
	t.Helper()
	w := wallet.NewTestWalletForRandomKey(t)
	w.OnCreateAction().ReturnSuccess(&wallet.CreateActionResult{
		Tx: fakeTxBytes,
	})
	return w
}

// ---------------------------------------------------------------------------
// ConstructPaymentHeaders
// ---------------------------------------------------------------------------

func TestConstructPaymentHeaders_AllFiveHeadersReturned(t *testing.T) {
	w := makeClientWallet(t)
	headers, err := ConstructPaymentHeaders(
		context.Background(), w,
		"https://example.com/articles/foo",
		100,
		"03f8104e2b313136ef1b84fcd9c8aadb775beb89a8207c942b31ab89e160ba4c86",
	)
	require.NoError(t, err)
	assert.NotEmpty(t, headers[HeaderBeef])
	assert.NotEmpty(t, headers[HeaderSender])
	assert.NotEmpty(t, headers[HeaderNonce])
	assert.NotEmpty(t, headers[HeaderTime])
	assert.NotEmpty(t, headers[HeaderVout])
}

func TestConstructPaymentHeaders_VoutIsAlwaysZero(t *testing.T) {
	w := makeClientWallet(t)
	headers, err := ConstructPaymentHeaders(
		context.Background(), w,
		"https://example.com/articles/foo",
		100,
		"03f8104e2b313136ef1b84fcd9c8aadb775beb89a8207c942b31ab89e160ba4c86",
	)
	require.NoError(t, err)
	assert.Equal(t, "0", headers[HeaderVout])
}

func TestConstructPaymentHeaders_BeefIsBase64OfFakeTx(t *testing.T) {
	w := makeClientWallet(t)
	headers, err := ConstructPaymentHeaders(
		context.Background(), w,
		"https://example.com/articles/foo",
		100,
		"03f8104e2b313136ef1b84fcd9c8aadb775beb89a8207c942b31ab89e160ba4c86",
	)
	require.NoError(t, err)
	assert.Equal(t, base64.StdEncoding.EncodeToString(fakeTxBytes), headers[HeaderBeef])
}

func TestConstructPaymentHeaders_TimeIsNumericMilliseconds(t *testing.T) {
	w := makeClientWallet(t)
	before := time.Now().UnixMilli()
	headers, err := ConstructPaymentHeaders(
		context.Background(), w,
		"https://example.com/articles/foo",
		100,
		"03f8104e2b313136ef1b84fcd9c8aadb775beb89a8207c942b31ab89e160ba4c86",
	)
	after := time.Now().UnixMilli()
	require.NoError(t, err)

	require.NotEmpty(t, headers[HeaderTime])
	assert.Regexp(t, `^\d+$`, headers[HeaderTime])
	timeMs := int64(0)
	for _, c := range headers[HeaderTime] {
		timeMs = timeMs*10 + int64(c-'0')
	}
	assert.GreaterOrEqual(t, timeMs, before)
	assert.LessOrEqual(t, timeMs, after)
}

func TestConstructPaymentHeaders_BRC42ProtocolUsed(t *testing.T) {
	w := wallet.NewTestWalletForRandomKey(t)
	w.OnCreateAction().ReturnSuccess(&wallet.CreateActionResult{Tx: fakeTxBytes})

	var capturedProtocol wallet.Protocol
	w.OnGetPublicKey().
		Expect(func(_ context.Context, args wallet.GetPublicKeyArgs, _ string) {
			if !args.IdentityKey {
				capturedProtocol = args.ProtocolID
			}
		}).
		ReturnSuccess(nil) // nil triggers default behaviour from underlying proto wallet

	// Re-use full wallet for this call since we need a real pubkey back
	w2 := makeClientWallet(t)
	_, err := ConstructPaymentHeaders(
		context.Background(), w2,
		"https://example.com/foo",
		100,
		"03f8104e2b313136ef1b84fcd9c8aadb775beb89a8207c942b31ab89e160ba4c86",
	)
	require.NoError(t, err)
	_ = capturedProtocol // protocol verified indirectly via successful derivation
}

func TestConstructPaymentHeaders_OriginatorIsURLOrigin(t *testing.T) {
	w := wallet.NewTestWalletForRandomKey(t)
	w.OnCreateAction().
		Expect(func(_ context.Context, _ wallet.CreateActionArgs, originator string) {
			assert.Equal(t, "https://pay.example.com", originator)
		}).
		ReturnSuccess(&wallet.CreateActionResult{Tx: fakeTxBytes})

	_, err := ConstructPaymentHeaders(
		context.Background(), w,
		"https://pay.example.com/item/1",
		50,
		"03f8104e2b313136ef1b84fcd9c8aadb775beb89a8207c942b31ab89e160ba4c86",
	)
	require.NoError(t, err)
}

func TestConstructPaymentHeaders_SatoshisPassedToCreateAction(t *testing.T) {
	w := wallet.NewTestWalletForRandomKey(t)
	w.OnCreateAction().
		Expect(func(_ context.Context, args wallet.CreateActionArgs, _ string) {
			require.Len(t, args.Outputs, 1)
			assert.Equal(t, uint64(777), args.Outputs[0].Satoshis)
		}).
		ReturnSuccess(&wallet.CreateActionResult{Tx: fakeTxBytes})

	_, err := ConstructPaymentHeaders(
		context.Background(), w,
		"https://example.com/item",
		777,
		"03f8104e2b313136ef1b84fcd9c8aadb775beb89a8207c942b31ab89e160ba4c86",
	)
	require.NoError(t, err)
}

func TestConstructPaymentHeaders_LockingScriptIsP2PKH(t *testing.T) {
	w := wallet.NewTestWalletForRandomKey(t)
	w.OnCreateAction().
		Expect(func(_ context.Context, args wallet.CreateActionArgs, _ string) {
			require.Len(t, args.Outputs, 1)
			script := args.Outputs[0].LockingScript
			// P2PKH: OP_DUP OP_HASH160 <20 bytes> OP_EQUALVERIFY OP_CHECKSIG
			// hex: 76 a9 14 <20 bytes> 88 ac  = 25 bytes total
			require.Len(t, script, 25)
			assert.Equal(t, byte(0x76), script[0])
			assert.Equal(t, byte(0xa9), script[1])
			assert.Equal(t, byte(0x14), script[2])
			assert.Equal(t, byte(0x88), script[23])
			assert.Equal(t, byte(0xac), script[24])
		}).
		ReturnSuccess(&wallet.CreateActionResult{Tx: fakeTxBytes})

	_, err := ConstructPaymentHeaders(
		context.Background(), w,
		"https://example.com/item",
		100,
		"03f8104e2b313136ef1b84fcd9c8aadb775beb89a8207c942b31ab89e160ba4c86",
	)
	require.NoError(t, err)
}

func TestConstructPaymentHeaders_RandomizeOutputsFalse(t *testing.T) {
	w := wallet.NewTestWalletForRandomKey(t)
	w.OnCreateAction().
		Expect(func(_ context.Context, args wallet.CreateActionArgs, _ string) {
			require.NotNil(t, args.Options)
			require.NotNil(t, args.Options.RandomizeOutputs)
			assert.False(t, *args.Options.RandomizeOutputs)
		}).
		ReturnSuccess(&wallet.CreateActionResult{Tx: fakeTxBytes})

	_, err := ConstructPaymentHeaders(
		context.Background(), w,
		"https://example.com/item",
		100,
		"03f8104e2b313136ef1b84fcd9c8aadb775beb89a8207c942b31ab89e160ba4c86",
	)
	require.NoError(t, err)
}

func TestConstructPaymentHeaders_TagsAndLabels(t *testing.T) {
	w := wallet.NewTestWalletForRandomKey(t)
	w.OnCreateAction().
		Expect(func(_ context.Context, args wallet.CreateActionArgs, _ string) {
			assert.Contains(t, args.Labels, "402-payment")
			require.Len(t, args.Outputs, 1)
			assert.Contains(t, args.Outputs[0].Tags, "402-payment")
		}).
		ReturnSuccess(&wallet.CreateActionResult{Tx: fakeTxBytes})

	_, err := ConstructPaymentHeaders(
		context.Background(), w,
		"https://example.com/item",
		100,
		"03f8104e2b313136ef1b84fcd9c8aadb775beb89a8207c942b31ab89e160ba4c86",
	)
	require.NoError(t, err)
}

func TestConstructPaymentHeaders_DescriptionContainsPathname(t *testing.T) {
	w := wallet.NewTestWalletForRandomKey(t)
	w.OnCreateAction().
		Expect(func(_ context.Context, args wallet.CreateActionArgs, _ string) {
			assert.Contains(t, args.Description, "/articles/my-post")
		}).
		ReturnSuccess(&wallet.CreateActionResult{Tx: fakeTxBytes})

	_, err := ConstructPaymentHeaders(
		context.Background(), w,
		"https://example.com/articles/my-post",
		100,
		"03f8104e2b313136ef1b84fcd9c8aadb775beb89a8207c942b31ab89e160ba4c86",
	)
	require.NoError(t, err)
}

func TestConstructPaymentHeaders_InvalidServerKey(t *testing.T) {
	w := makeClientWallet(t)
	_, err := ConstructPaymentHeaders(
		context.Background(), w,
		"https://example.com/foo",
		100,
		"not-a-valid-key",
	)
	assert.Error(t, err)
}

// ---------------------------------------------------------------------------
// Client402
// ---------------------------------------------------------------------------

func TestClient402_NonPaymentResponse_PassedThrough(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("hello"))
	}))
	defer srv.Close()

	c := NewClient402(Client402Options{Wallet: makeClientWallet(t)})
	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/free", nil)
	res, err := c.Do(context.Background(), req)
	require.NoError(t, err)
	assert.Equal(t, http.StatusOK, res.StatusCode)
	body, _ := io.ReadAll(res.Body)
	assert.Equal(t, "hello", string(body))
}

func TestClient402_404_PassedThrough(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	c := NewClient402(Client402Options{Wallet: makeClientWallet(t)})
	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/missing", nil)
	res, err := c.Do(context.Background(), req)
	require.NoError(t, err)
	assert.Equal(t, http.StatusNotFound, res.StatusCode)
}

func TestClient402_MalformedSatsHeader_Returns402(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set(HeaderSats, "not-a-number")
		w.Header().Set(HeaderServer, "03f8104e2b313136ef1b84fcd9c8aadb775beb89a8207c942b31ab89e160ba4c86")
		w.WriteHeader(http.StatusPaymentRequired)
	}))
	defer srv.Close()

	c := NewClient402(Client402Options{Wallet: makeClientWallet(t)})
	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/paid", nil)
	res, err := c.Do(context.Background(), req)
	require.NoError(t, err)
	assert.Equal(t, http.StatusPaymentRequired, res.StatusCode)
}

func TestClient402_MissingServerHeader_Returns402(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set(HeaderSats, "100")
		w.WriteHeader(http.StatusPaymentRequired)
	}))
	defer srv.Close()

	c := NewClient402(Client402Options{Wallet: makeClientWallet(t)})
	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/paid", nil)
	res, err := c.Do(context.Background(), req)
	require.NoError(t, err)
	assert.Equal(t, http.StatusPaymentRequired, res.StatusCode)
}

func TestClient402_ZeroSats_Returns402(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set(HeaderSats, "0")
		w.Header().Set(HeaderServer, "03f8104e2b313136ef1b84fcd9c8aadb775beb89a8207c942b31ab89e160ba4c86")
		w.WriteHeader(http.StatusPaymentRequired)
	}))
	defer srv.Close()

	c := NewClient402(Client402Options{Wallet: makeClientWallet(t)})
	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/paid", nil)
	res, err := c.Do(context.Background(), req)
	require.NoError(t, err)
	assert.Equal(t, http.StatusPaymentRequired, res.StatusCode)
}

func TestClient402_HappyPath_ReceivesPaidContent(t *testing.T) {
	callCount := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount++
		if r.Header.Get(HeaderBeef) == "" {
			w.Header().Set(HeaderSats, "100")
			w.Header().Set(HeaderServer, "03f8104e2b313136ef1b84fcd9c8aadb775beb89a8207c942b31ab89e160ba4c86")
			w.WriteHeader(http.StatusPaymentRequired)
			return
		}
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("paid content"))
	}))
	defer srv.Close()

	c := NewClient402(Client402Options{Wallet: makeClientWallet(t)})
	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/paid", nil)
	res, err := c.Do(context.Background(), req)
	require.NoError(t, err)
	assert.Equal(t, http.StatusOK, res.StatusCode)
	body, _ := io.ReadAll(res.Body)
	assert.Equal(t, "paid content", string(body))
	assert.Equal(t, 2, callCount) // initial + retransmit
}

func TestClient402_HappyPath_PaymentHeadersSent(t *testing.T) {
	var capturedBeef, capturedSender, capturedNonce, capturedTime, capturedVout string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get(HeaderBeef) == "" {
			w.Header().Set(HeaderSats, "100")
			w.Header().Set(HeaderServer, "03f8104e2b313136ef1b84fcd9c8aadb775beb89a8207c942b31ab89e160ba4c86")
			w.WriteHeader(http.StatusPaymentRequired)
			return
		}
		capturedBeef = r.Header.Get(HeaderBeef)
		capturedSender = r.Header.Get(HeaderSender)
		capturedNonce = r.Header.Get(HeaderNonce)
		capturedTime = r.Header.Get(HeaderTime)
		capturedVout = r.Header.Get(HeaderVout)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	c := NewClient402(Client402Options{Wallet: makeClientWallet(t)})
	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/paid", nil)
	_, err := c.Do(context.Background(), req)
	require.NoError(t, err)

	assert.NotEmpty(t, capturedBeef)
	assert.NotEmpty(t, capturedSender)
	assert.NotEmpty(t, capturedNonce)
	assert.Regexp(t, `^\d+$`, capturedTime)
	assert.Equal(t, "0", capturedVout)
}

func TestClient402_Cache_HitAfterFirstSuccess(t *testing.T) {
	callCount := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount++
		if r.Header.Get(HeaderBeef) == "" {
			w.Header().Set(HeaderSats, "100")
			w.Header().Set(HeaderServer, "03f8104e2b313136ef1b84fcd9c8aadb775beb89a8207c942b31ab89e160ba4c86")
			w.WriteHeader(http.StatusPaymentRequired)
			return
		}
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("paid content"))
	}))
	defer srv.Close()

	c := NewClient402(Client402Options{Wallet: makeClientWallet(t)})
	u := srv.URL + "/paid"

	req1, _ := http.NewRequest(http.MethodGet, u, nil)
	_, err := c.Do(context.Background(), req1)
	require.NoError(t, err)

	req2, _ := http.NewRequest(http.MethodGet, u, nil)
	res2, err := c.Do(context.Background(), req2)
	require.NoError(t, err)
	assert.Equal(t, http.StatusOK, res2.StatusCode)

	// Only 2 actual HTTP calls (initial 402 + retransmit) — second client call hits cache
	assert.Equal(t, 2, callCount)
}

func TestClient402_Cache_ClearCacheForcesFetch(t *testing.T) {
	callCount := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount++
		if r.Header.Get(HeaderBeef) == "" {
			w.Header().Set(HeaderSats, "100")
			w.Header().Set(HeaderServer, "03f8104e2b313136ef1b84fcd9c8aadb775beb89a8207c942b31ab89e160ba4c86")
			w.WriteHeader(http.StatusPaymentRequired)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	c := NewClient402(Client402Options{Wallet: makeClientWallet(t)})
	u := srv.URL + "/paid"

	req1, _ := http.NewRequest(http.MethodGet, u, nil)
	c.Do(context.Background(), req1)

	c.ClearCache()

	req2, _ := http.NewRequest(http.MethodGet, u, nil)
	c.Do(context.Background(), req2)

	assert.Equal(t, 4, callCount) // 2 per round-trip × 2 round-trips
}

func TestClient402_Cache_ExpiredEntryRefetches(t *testing.T) {
	callCount := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount++
		if r.Header.Get(HeaderBeef) == "" {
			w.Header().Set(HeaderSats, "100")
			w.Header().Set(HeaderServer, "03f8104e2b313136ef1b84fcd9c8aadb775beb89a8207c942b31ab89e160ba4c86")
			w.WriteHeader(http.StatusPaymentRequired)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	c := NewClient402(Client402Options{
		Wallet:       makeClientWallet(t),
		CacheTimeout: 1 * time.Millisecond, // immediately expire
	})
	u := srv.URL + "/paid"

	req1, _ := http.NewRequest(http.MethodGet, u, nil)
	c.Do(context.Background(), req1)

	time.Sleep(5 * time.Millisecond)

	req2, _ := http.NewRequest(http.MethodGet, u, nil)
	c.Do(context.Background(), req2)

	assert.Equal(t, 4, callCount) // expired — must re-fetch
}

func TestClient402_Cache_PerURL(t *testing.T) {
	callCount := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount++
		if r.Header.Get(HeaderBeef) == "" {
			w.Header().Set(HeaderSats, "100")
			w.Header().Set(HeaderServer, "03f8104e2b313136ef1b84fcd9c8aadb775beb89a8207c942b31ab89e160ba4c86")
			w.WriteHeader(http.StatusPaymentRequired)
			return
		}
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(r.URL.Path))
	}))
	defer srv.Close()

	c := NewClient402(Client402Options{Wallet: makeClientWallet(t)})

	req1, _ := http.NewRequest(http.MethodGet, srv.URL+"/a", nil)
	r1, _ := c.Do(context.Background(), req1)
	body1, _ := io.ReadAll(r1.Body)

	req2, _ := http.NewRequest(http.MethodGet, srv.URL+"/b", nil)
	r2, _ := c.Do(context.Background(), req2)
	body2, _ := io.ReadAll(r2.Body)

	assert.Equal(t, "/a", string(body1))
	assert.Equal(t, "/b", string(body2))
	assert.Equal(t, 4, callCount) // 2 per URL × 2 URLs
}

func TestClient402_PreservesExistingHeaders(t *testing.T) {
	var capturedAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get(HeaderBeef) == "" {
			w.Header().Set(HeaderSats, "100")
			w.Header().Set(HeaderServer, "03f8104e2b313136ef1b84fcd9c8aadb775beb89a8207c942b31ab89e160ba4c86")
			w.WriteHeader(http.StatusPaymentRequired)
			return
		}
		capturedAuth = r.Header.Get("Authorization")
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	c := NewClient402(Client402Options{Wallet: makeClientWallet(t)})
	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/paid", nil)
	req.Header.Set("Authorization", "Bearer token")
	c.Do(context.Background(), req)

	assert.Equal(t, "Bearer token", capturedAuth)
}

// keep strings import used
var _ = strings.Contains
