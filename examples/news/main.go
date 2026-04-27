package main

import (
	"embed"
	"fmt"
	"log/slog"
	"net/http"
	"os"

	pay402 "github.com/bsv-blockchain/go-402-pay"
	ec "github.com/bsv-blockchain/go-sdk/primitives/ec"
	sdk "github.com/bsv-blockchain/go-sdk/wallet"

	"github.com/bsv-blockchain/go-wallet-toolbox/pkg/defs"
	"github.com/bsv-blockchain/go-wallet-toolbox/pkg/infra"
	"github.com/bsv-blockchain/go-wallet-toolbox/pkg/storage"
	"github.com/bsv-blockchain/go-wallet-toolbox/pkg/wallet"
)

// Here we define a hardcoded XPriv and Storage URL for the example so no config file is needed.
const exampleXPriv = "xprv9s21ZrQH143K3WYAquX13GWNfPShBx5XT98kBDMQpxz5p1EYJ8fsqwQCkKuJyB7"
const exampleStorageURL = "http://localhost:8100"

//go:embed static/*
var staticFS embed.FS

func main() {
	logger := setupLogging()

	wallet, cleanup, err := setupWallet()
	if err != nil {
		panic(fmt.Errorf("failed to setup wallet: %w", err))
	}
	defer cleanup()

	logger.Info("Wallet initialized", "wallet", wallet)

	mux := setupRoutes()
	handler := setupMiddleware(wallet, mux)

	logger.Info("Routes and middleware set up")
	logger.Info("Server listening on http://localhost:8080")

	if err := http.ListenAndServe(":8080", handler); err != nil {
		panic(err)
	}
}

func setupLogging() *slog.Logger {
	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelDebug}))
	slog.SetDefault(logger)

	return logger
}

func setupRoutes() *http.ServeMux {
	mux := http.NewServeMux()

	mux.Handle("/static/", http.FileServer(http.FS(staticFS)))

	// Free homepage endpoint
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		b, err := staticFS.ReadFile("static/index.html")
		if err != nil {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/html")
		w.Write(b)
	})

	// Free article
	mux.HandleFunc("/article/1", func(w http.ResponseWriter, r *http.Request) {
		b, err := staticFS.ReadFile("static/article_free.html")
		if err != nil {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/html")
		w.Write(b)
	})

	// Paid article
	mux.HandleFunc("/article/premium", func(w http.ResponseWriter, r *http.Request) {
		b, err := staticFS.ReadFile("static/article_premium.html")
		if err != nil {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/html")
		w.Write(b)
	})

	return mux
}

func setupMiddleware(wallet *wallet.Wallet, mux *http.ServeMux) http.Handler {
	return pay402.PaymentMiddleware(pay402.MiddlewareOptions{
		Wallet: wallet,
		CalculatePrice: func(path string) int {
			if path == "/article/premium" {
				return 1
			}
			return 0
		},
		Logger: slog.New(slog.NewTextHandler(os.Stdout, nil)),
	}, mux)
}

func setupWallet() (*wallet.Wallet, func(), error) {
	// Define private key for the wallet
	aliceKey, _ := ec.PrivateKeyFromBytes([]byte(exampleXPriv))
	if aliceKey == nil {
		return nil, nil, fmt.Errorf("cannot create Alice private key")
	}

	cfg := infra.Defaults()
	cfg.BSVNetwork = defs.NetworkMainnet

	// Create a new proto wallet
	pw, err := sdk.NewCompletedProtoWallet(aliceKey)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to create proto wallet: %w", err)
	}

	// Create a new storage provider client
	wspc, cleanup, err := storage.NewClient(exampleStorageURL, pw)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to create storage provider client: %w", err)
	}

	// Create a new wallet
	activeWallet, err := wallet.New(cfg.BSVNetwork, aliceKey, wspc)
	if err != nil {
		cleanup()
		return nil, nil, fmt.Errorf("failed to create server wallet: %w", err)
	}

	return activeWallet, func() {
		cleanup()
		activeWallet.Close()
	}, nil
}
