package pay402

import (
	"context"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"sync"
	"time"

	"crypto/rand"
	ec "github.com/bsv-blockchain/go-sdk/primitives/ec"
	"github.com/bsv-blockchain/go-sdk/wallet"
)

// ConstructPaymentHeaders builds the five BRC-121 client-to-server headers
// for a payment of satoshis to the server at url, without performing any HTTP request.
//
// This is the low-level primitive useful for service workers, custom transports,
// or any scenario where you need to attach payment headers manually.
func ConstructPaymentHeaders(
	ctx context.Context,
	w wallet.Interface,
	rawURL string,
	satoshis uint64,
	serverIdentityKey string,
) (map[string]string, error) {
	parsed, err := parseOriginAndPath(rawURL)
	if err != nil {
		return nil, fmt.Errorf("invalid url: %w", err)
	}
	originator := parsed.origin

	// Generate 8 random bytes for the nonce (derivation prefix)
	nonceBytes := make([]byte, 8)
	if _, err := rand.Read(nonceBytes); err != nil {
		return nil, fmt.Errorf("failed to generate nonce: %w", err)
	}
	nonce := base64.StdEncoding.EncodeToString(nonceBytes)

	// time is unix milliseconds as a decimal string
	timeStr := strconv.FormatInt(time.Now().UnixMilli(), 10)
	// timeSuffixB64 = base64(utf8(timeStr)) — mirrors the TypeScript implementation
	timeSuffixB64 := base64.StdEncoding.EncodeToString([]byte(timeStr))

	// Parse server identity key
	serverKey, err := ec.PublicKeyFromString(serverIdentityKey)
	if err != nil {
		return nil, fmt.Errorf("invalid server identity key: %w", err)
	}

	// Derive recipient public key via BRC-42
	derivedKeyResult, err := w.GetPublicKey(ctx, wallet.GetPublicKeyArgs{
		EncryptionArgs: wallet.EncryptionArgs{
			ProtocolID:   BRC29ProtocolID,
			KeyID:        nonce + " " + timeSuffixB64,
			Counterparty: wallet.Counterparty{Type: wallet.CounterpartyTypeOther, Counterparty: serverKey},
		},
	}, originator)
	if err != nil {
		return nil, fmt.Errorf("failed to derive recipient key: %w", err)
	}

	// Build P2PKH locking script: OP_DUP OP_HASH160 <pubKeyHash> OP_EQUALVERIFY OP_CHECKSIG
	pkh := derivedKeyResult.PublicKey.Hash()
	lockingScript, err := hex.DecodeString("76a914" + hex.EncodeToString(pkh) + "88ac")
	if err != nil {
		return nil, fmt.Errorf("failed to build locking script: %w", err)
	}

	// Get sender identity key
	senderResult, err := w.GetPublicKey(ctx, wallet.GetPublicKeyArgs{IdentityKey: true}, originator)
	if err != nil {
		return nil, fmt.Errorf("failed to get sender identity key: %w", err)
	}
	senderIdentityKey := senderResult.PublicKey.ToDERHex()

	// Create payment transaction
	customInstructions, _ := json.Marshal(map[string]string{
		"derivationPrefix":  nonce,
		"derivationSuffix":  timeSuffixB64,
		"serverIdentityKey": serverIdentityKey,
	})
	falseVal := false
	actionResult, err := w.CreateAction(ctx, wallet.CreateActionArgs{
		Description: fmt.Sprintf("Paid Content: %s", parsed.path),
		Outputs: []wallet.CreateActionOutput{
			{
				Satoshis:           satoshis,
				LockingScript:      lockingScript,
				OutputDescription:  "402 web payment",
				CustomInstructions: string(customInstructions),
				Tags:               []string{"402-payment"},
			},
		},
		Labels: []string{"402-payment"},
		Options: &wallet.CreateActionOptions{
			RandomizeOutputs: &falseVal,
		},
	}, originator)
	if err != nil {
		return nil, fmt.Errorf("failed to create payment action: %w", err)
	}

	txBase64 := base64.StdEncoding.EncodeToString(actionResult.Tx)

	return map[string]string{
		HeaderBeef:   txBase64,
		HeaderSender: senderIdentityKey,
		HeaderNonce:  nonce,
		HeaderTime:   timeStr,
		HeaderVout:   "0",
	}, nil
}

// cacheEntry holds a cached response body and metadata.
type cacheEntry struct {
	body       string
	statusCode int
	header     http.Header
	storedAt   time.Time
}

// Client402 is an HTTP client that automatically handles 402 Payment Required
// responses by constructing a BRC-121 payment and retrying the request.
type Client402 struct {
	wallet       wallet.Interface
	httpClient   *http.Client
	cacheTimeout time.Duration
	mu           sync.Mutex
	cache        map[string]*cacheEntry
}

// Client402Options configures a Client402.
type Client402Options struct {
	// Wallet is the client's wallet instance.
	Wallet wallet.Interface
	// HTTPClient overrides the HTTP client used for requests. Defaults to http.DefaultClient.
	HTTPClient *http.Client
	// CacheTimeout is how long a paid response is served from cache. Defaults to 30 minutes.
	CacheTimeout time.Duration
}

// NewClient402 creates a new Client402.
func NewClient402(opts Client402Options) *Client402 {
	hc := opts.HTTPClient
	if hc == nil {
		hc = http.DefaultClient
	}
	ct := opts.CacheTimeout
	if ct <= 0 {
		ct = 30 * time.Minute
	}
	return &Client402{
		wallet:       opts.Wallet,
		httpClient:   hc,
		cacheTimeout: ct,
		cache:        make(map[string]*cacheEntry),
	}
}

// ClearCache evicts all cached responses.
func (c *Client402) ClearCache() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.cache = make(map[string]*cacheEntry)
}

// Do performs an HTTP request, automatically handling 402 responses with payment.
// On success the response body is cached by URL for CacheTimeout.
func (c *Client402) Do(ctx context.Context, req *http.Request) (*http.Response, error) {
	url := req.URL.String()

	// Check cache
	c.mu.Lock()
	entry, found := c.cache[url]
	c.mu.Unlock()
	if found && time.Since(entry.storedAt) < c.cacheTimeout {
		return cachedResponse(entry), nil
	}

	// Initial request
	res, err := c.httpClient.Do(req.WithContext(ctx))
	if err != nil {
		return nil, err
	}
	if res.StatusCode != http.StatusPaymentRequired {
		return res, nil
	}

	// Read 402 headers
	satsHeader := res.Header.Get(HeaderSats)
	serverHeader := res.Header.Get(HeaderServer)
	if satsHeader == "" || serverHeader == "" {
		return res, nil
	}
	satoshis, err := strconv.ParseUint(satsHeader, 10, 64)
	if err != nil || satoshis == 0 {
		return res, nil
	}
	res.Body.Close()

	// Construct payment headers
	paymentHeaders, err := ConstructPaymentHeaders(ctx, c.wallet, url, satoshis, serverHeader)
	if err != nil {
		return nil, fmt.Errorf("failed to construct payment headers: %w", err)
	}

	// Clone the original request for retransmission with payment headers
	paidReq, err := cloneRequest(req, paymentHeaders)
	if err != nil {
		return nil, err
	}

	paidRes, err := c.httpClient.Do(paidReq.WithContext(ctx))
	if err != nil {
		return nil, err
	}

	// Cache successful responses
	if paidRes.StatusCode >= 200 && paidRes.StatusCode < 300 {
		body, err := io.ReadAll(paidRes.Body)
		paidRes.Body.Close()
		if err != nil {
			return nil, err
		}
		e := &cacheEntry{
			body:       string(body),
			statusCode: paidRes.StatusCode,
			header:     paidRes.Header.Clone(),
			storedAt:   time.Now(),
		}
		c.mu.Lock()
		c.cache[url] = e
		c.mu.Unlock()
		return cachedResponse(e), nil
	}

	return paidRes, nil
}

// --- helpers ----------------------------------------------------------------

type parsedURL struct {
	origin string
	path   string
}

func parseOriginAndPath(rawURL string) (parsedURL, error) {
	req, err := http.NewRequest(http.MethodGet, rawURL, nil)
	if err != nil {
		return parsedURL{}, err
	}
	scheme := req.URL.Scheme
	if scheme == "" {
		scheme = "https"
	}
	origin := scheme + "://" + req.URL.Host
	path := req.URL.Path
	if path == "" {
		path = "/"
	}
	return parsedURL{origin: origin, path: path}, nil
}

func cloneRequest(original *http.Request, extraHeaders map[string]string) (*http.Request, error) {
	clone, err := http.NewRequest(original.Method, original.URL.String(), original.Body)
	if err != nil {
		return nil, err
	}
	// Copy original headers
	for k, vals := range original.Header {
		for _, v := range vals {
			clone.Header.Add(k, v)
		}
	}
	// Apply payment headers (override any matching originals)
	for k, v := range extraHeaders {
		clone.Header.Set(k, v)
	}
	return clone, nil
}

func cachedResponse(e *cacheEntry) *http.Response {
	r := &http.Response{
		StatusCode: e.statusCode,
		Header:     e.header.Clone(),
		Body:       io.NopCloser(readerFromString(e.body)),
	}
	return r
}

type stringReader struct {
	s   string
	pos int
}

func readerFromString(s string) io.Reader { return &stringReader{s: s} }

func (r *stringReader) Read(p []byte) (int, error) {
	if r.pos >= len(r.s) {
		return 0, io.EOF
	}
	n := copy(p, r.s[r.pos:])
	r.pos += n
	return n, nil
}
