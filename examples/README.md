# 402 Payment Examples

This directory contains example servers that implement the BRC-121 Simple 402 Payments flow using `go-402-pay`.

> [!WARNING]
> **MAINNET & WALLET CONFIGURATION**
> Both of these examples are configured to run natively on the **Bitcoin SV Mainnet**. They instantiate a real `go-wallet-toolbox` wallet backed by a localized SQLite database and connect directly to ARC Broadcaster nodes to merge and validate payments. 
> 
> They currently use a hardcoded `exampleXPriv` constant to boot without extra configuration. **DO NOT send your own real Mainnet BSV to this hardcoded `exampleXPriv`!** Anyone viewing this repository can derive its keys. Always generate your own secure `xpriv` if launching securely.

## Examples

### 1. News Server (`/news`)
A full-stack web application showcasing a micropayment paywall. It bundles stylized sleek HTML/CSS directly via `//go:embed` and charges exactly 100 satoshis to unlock premium content.

To run:
```bash
cd news
go run main.go
```
The server listens on `http://localhost:8080`.

### 2. Media Marketplace (`/media`)
A dynamic pricing server that returns genuine binary media payloads to clients who fulfill the 402 paywall. It bundles a stunning `//go:embed` HTML frontend and dynamically charges depending on the asset:
- Premium Photos (`.jpg`): 50 sats
- Premium Music (`.mp3`): 200 sats
- Premium Videos (`.mp4`): 500 sats

To run:
```bash
cd media
go run main.go
```
The server listens on `http://localhost:8081`.

## How to Test the Paywalls

Once a server is running, navigating to a premium endpoint in a standard web browser (like `http://localhost:8080/article/premium`) will result in an empty or basic HTTP 402 response, because a standard browser does not automatically understand the BRC-121 payment handshake.

To successfully access the paid content, a client must fulfill the payment using either:
1. **The 402 Chrome Extension**: Install the [402-extension](https://github.com/bsv-blockchain/402-extension) in your browser. Configure it with a funded wallet. When you navigate to the premium page, the extension will intercept the 402 response, construct the transaction, append the required headers, and automatically retry the request to load the content.
2. **The Go Client (`pay402.Client402`)**: Write a small script using the embedded client wrapper around `http.Client`.

### Using the Go Client
You can interact with these example servers using the `go-402-pay` client abstraction in a Go script:

```go
package main

import (
	"context"
	"fmt"
	"io"
	"net/http"

	pay402 "github.com/bsv-blockchain/go-402-pay"
	"github.com/bsv-blockchain/go-sdk/wallet"
)

func main() {
	// Instantiate a wallet with a funded private key to pay the fees
	w := wallet.NewTestWallet(...) // use arcwallet.NewWallet() for production

	client := pay402.NewClient402(pay402.Client402Options{
		Wallet: w,
	})

	// Request the premium endpoint. The client will handle the 402 handshake automatically.
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, "http://localhost:8080/article/premium", nil)
	resp, err := client.Do(req)
	if err != nil {
		panic(err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	fmt.Println("Response:", string(body))
}
```
