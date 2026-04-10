# go-402-pay

Go port of [`@bsv/402-pay`](https://github.com/bsv-blockchain/ts-402-pay) — server middleware and client helpers implementing [BRC-121 Simple 402 Payments](https://brc.dev/121) for BSV micropayments over HTTP. The Chrome extension [`402-extension`](https://github.com/bsv-blockchain/402-extension) provides browser-level automation of the same protocol.

When a client requests a paid endpoint, the server responds with HTTP 402 and the required satoshi amount. The client constructs a BSV transaction, attaches it as request headers, and retries. The server validates the payment via its wallet and either accepts or rejects it.

## Packages

| Package | Import path | Framework |
|---------|-------------|-----------|
| `pay402` | `github.com/bsv-blockchain/go-402-pay` | `net/http` |
| `pay402fiber` | `github.com/bsv-blockchain/go-402-pay/fiber` | [Fiber](https://gofiber.io) |
| `pay402gin` | `github.com/bsv-blockchain/go-402-pay/gin` | [Gin](https://gin-gonic.com) |
| `pay402echo` | `github.com/bsv-blockchain/go-402-pay/echo` | [Echo](https://echo.labstack.com) |

## Server usage (`net/http`)

```go
import pay402 "github.com/bsv-blockchain/go-402-pay"

mux := http.NewServeMux()
mux.HandleFunc("/article", articleHandler)

handler := pay402.PaymentMiddleware(pay402.MiddlewareOptions{
    Wallet: myWallet,
    CalculatePrice: func(path string) int {
        if path == "/article" {
            return 100 // 100 satoshis
        }
        return 0 // free
    },
}, mux)

http.ListenAndServe(":8080", handler)
```

Inside a handler, retrieve payment details from the context:

```go
func articleHandler(w http.ResponseWriter, r *http.Request) {
    result, price, ok := pay402.PaymentFromContext(r.Context())
    if ok {
        fmt.Printf("Paid %d sats, txid: %s\n", price, result.TXID)
    }
    fmt.Fprintln(w, "Here is your article.")
}
```

## Server usage (Fiber / Gin / Echo)

Each sub-package exposes the same `PaymentMiddleware` + `PaymentFromContext` API adapted to its framework.

**Fiber**

```go
import pay402fiber "github.com/bsv-blockchain/go-402-pay/fiber"

app.Use(pay402fiber.PaymentMiddleware(pay402fiber.Options{
    Wallet:         myWallet,
    CalculatePrice: func(path string) int { return 100 },
}))

app.Get("/paid", func(c *fiber.Ctx) error {
    result, price, ok := pay402fiber.PaymentFromContext(c)
    _ = result; _ = price; _ = ok
    return c.SendString("paid content")
})
```

**Gin**

```go
import pay402gin "github.com/bsv-blockchain/go-402-pay/gin"

r.Use(pay402gin.PaymentMiddleware(pay402gin.Options{
    Wallet:         myWallet,
    CalculatePrice: func(path string) int { return 100 },
}))
```

**Echo**

```go
import pay402echo "github.com/bsv-blockchain/go-402-pay/echo"

e.Use(pay402echo.PaymentMiddleware(pay402echo.Options{
    Wallet:         myWallet,
    CalculatePrice: func(path string) int { return 100 },
}))
```

## Client usage

`Client402` wraps `http.Client` and automatically handles 402 responses:

```go
import pay402 "github.com/bsv-blockchain/go-402-pay"

client := pay402.NewClient402(pay402.Client402Options{
    Wallet: myWallet,
})

req, _ := http.NewRequestWithContext(ctx, http.MethodGet, "https://example.com/article", nil)
resp, err := client.Do(ctx, req)
```

On a 402 response the client constructs and attaches a BRC-121 payment automatically, then retries. Successful paid responses are cached for 30 minutes by default (configurable via `CacheTimeout`).

To build payment headers without making a request (e.g. for service workers):

```go
headers, err := pay402.ConstructPaymentHeaders(ctx, myWallet, url, satoshis, serverIdentityKey)
```

## Protocol

BRC-121 uses five custom HTTP headers:

| Direction | Header | Description |
|-----------|--------|-------------|
| Server → Client | `x-bsv-sats` | Required satoshi amount |
| Server → Client | `x-bsv-server` | Server identity public key (DER hex) |
| Client → Server | `x-bsv-beef` | Base64-encoded BEEF transaction |
| Client → Server | `x-bsv-sender` | Client identity public key (DER hex) |
| Client → Server | `x-bsv-nonce` | Base64-encoded derivation prefix (8 random bytes) |
| Client → Server | `x-bsv-time` | Unix timestamp in milliseconds |
| Client → Server | `x-bsv-vout` | Payment output index |

Keys are derived using BRC-42 / BRC-29. The server internalizes the transaction via its `wallet.Interface`; replay attacks are rejected because `InternalizeAction` returns `Accepted: false` for already-seen transactions.

The default payment validity window is 30 seconds (`DefaultPaymentWindowMs`).

## Related

- [`@bsv/402-pay`](https://github.com/bsv-blockchain/ts-402-pay) — TypeScript original (Express middleware + fetch wrapper)
- [`402-extension`](https://github.com/bsv-blockchain/402-extension) — Chrome extension that intercepts 402 responses and pays automatically at the browser level
- [BRC-121](https://brc.dev/121) — Simple 402 Payments spec
- [BRC-29](https://brc.dev/29) — Payment key derivation
- [BRC-95](https://brc.dev/95) — BEEF transaction format

## Requirements

- Go 1.21+
- A `wallet.Interface` implementation (e.g. from [go-sdk](https://github.com/bsv-blockchain/go-sdk))
