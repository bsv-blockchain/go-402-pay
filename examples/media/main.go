package main

import (
	"bytes"
	"context"
	"embed"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"time"

	pay402 "github.com/bsv-blockchain/go-402-pay"
	ec "github.com/bsv-blockchain/go-sdk/primitives/ec"
	sdk "github.com/bsv-blockchain/go-sdk/wallet"

	"github.com/bsv-blockchain/go-wallet-toolbox/pkg/defs"
	"github.com/bsv-blockchain/go-wallet-toolbox/pkg/infra"
	"github.com/bsv-blockchain/go-wallet-toolbox/pkg/services"
	"github.com/bsv-blockchain/go-wallet-toolbox/pkg/storage"
	"github.com/bsv-blockchain/go-wallet-toolbox/pkg/wallet"
	"github.com/bsv-blockchain/go-wallet-toolbox/pkg/wdk"
)

// Here we define a hardcoded XPriv for the example so no config file is needed.
const exampleXPriv = "xprv9s21ZrQH143K3WYAquX13GWNfPShBx5XT98kBDMQpxz5p1EYJ8fsqwQCkKuJyB7"

//go:embed static/*
var staticFS embed.FS

func main() {
	ctx := context.Background()
	logger := setupLogging()

	wallet, cleanup, err := setupWallet(ctx, logger)
	if err != nil {
		panic(fmt.Errorf("failed to setup wallet: %w", err))
	}
	defer cleanup()

	logger.Info("Wallet initialized", "wallet", wallet)

	mux := setupRoutes()
	handler := setupMiddleware(wallet, mux)

	logger.Info("Routes and middleware set up")
	logger.Info("Media Server listening on http://localhost:8081")

	if err := http.ListenAndServe(":8081", handler); err != nil {
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

	// Landing Page
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

	// Dynamic media endpoints
	mux.HandleFunc("/media/", func(w http.ResponseWriter, r *http.Request) {
		// Since this is behind a 402 paywall, reaching this point means
		// the client has successfully paid the required satoshis!

		// Set a cookie so that subsequent requests (like Chrome's range requests
		// for video streaming) can bypass the payment middleware.
		cookieName := "paid_" + strings.ReplaceAll(r.URL.Path, "/", "_")
		http.SetCookie(w, &http.Cookie{
			Name:     cookieName,
			Value:    "true",
			Path:     r.URL.Path,
			MaxAge:   3600, // 1 hour access
			HttpOnly: true,
		})

		resourceType := "unknown"
		if strings.HasSuffix(r.URL.Path, ".jpg") {
			resourceType = "photo"
			w.Header().Set("Content-Type", "image/jpeg")
		} else if strings.HasSuffix(r.URL.Path, ".mp3") {
			resourceType = "music"
			w.Header().Set("Content-Type", "audio/mpeg")
		} else if strings.HasSuffix(r.URL.Path, ".mp4") {
			resourceType = "video"
			w.Header().Set("Content-Type", "video/mp4")
		} else {
			w.Header().Set("Content-Type", "text/plain")
		}

		fileName := strings.TrimPrefix(r.URL.Path, "/media/")
		b, err := staticFS.ReadFile("static/" + fileName)
		if err != nil {
			msg := fmt.Sprintf("[%s content representing %s - you have successfully paid for this media!]", resourceType, r.URL.Path)
			w.Write([]byte(msg))
			return
		}

		http.ServeContent(w, r, fileName, time.Time{}, bytes.NewReader(b))
	})

	return mux
}

func setupMiddleware(realWallet *wallet.Wallet, mux *http.ServeMux) http.Handler {
	// Initialize the 402 middleware with dynamic pricing logic
	paymentMw := pay402.PaymentMiddleware(pay402.MiddlewareOptions{
		Wallet: realWallet,
		CalculatePrice: func(path string) int {
			if strings.HasSuffix(path, ".jpg") {
				return 1 // Photos: 50 sats
			}
			if strings.HasSuffix(path, ".mp3") {
				return 200 // Audio: 200 sats
			}
			if strings.HasSuffix(path, ".mp4") {
				return 500 // Video: 500 sats
			}
			return 0 // Default: free
		},
		Logger: slog.New(slog.NewTextHandler(os.Stdout, nil)),
	}, mux)

	// Wrap with cookie checker to bypass payment for subsequent media range requests
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cookieName := "paid_" + strings.ReplaceAll(r.URL.Path, "/", "_")
		if cookie, err := r.Cookie(cookieName); err == nil && cookie.Value == "true" {
			mux.ServeHTTP(w, r)
			return
		}
		paymentMw.ServeHTTP(w, r)
	})
}

func setupWallet(ctx context.Context, logger *slog.Logger) (*wallet.Wallet, func(), error) {
	// Define private key for the wallet
	aliceKey, _ := ec.PrivateKeyFromBytes([]byte(exampleXPriv))
	if aliceKey == nil {
		return nil, nil, fmt.Errorf("cannot create Alice private key")
	}

	// Configure the default ARC Broadcaster / Network Services
	cfg := infra.Defaults()
	cfg.BSVNetwork = defs.NetworkMainnet
	cfg.Services = defs.DefaultServicesConfig(defs.NetworkMainnet)
	cfg.DBConfig.SQLite.ConnectionString = "media-server.sqlite"

	// Create background services (this initiates ARC integration and others)
	activeServices := services.New(logger, cfg.Services)

	// Wrap the instantiation logic to use a local DB (or memory) with our broadcaster.
	activeWallet, err := wallet.NewWithStorageFactory(
		cfg.BSVNetwork,
		aliceKey,
		func(userWallet sdk.Interface) (wdk.WalletStorageProvider, func(), error) {

			options := append(
				infra.GORMProviderOptionsFromConfig(&cfg),
				storage.WithLogger(logger),
				storage.WithBackgroundBroadcasterContext(ctx),
			)

			// NewGORMProvider supports sqlite out of the box and natively spins up a broadcaster if configured.
			store, err := storage.NewGORMProvider(cfg.BSVNetwork, activeServices, options...)
			if err != nil {
				return nil, nil, err
			}

			identityKey := aliceKey.PubKey()
			storeIdentityKey, _ := wdk.IdentityKey(identityKey.ToDERHex())
			if _, err := store.Migrate(ctx, "media_example_wallet", storeIdentityKey); err != nil {
				return nil, nil, err
			}

			cleanup := func() {
				store.Stop()
			}
			return store, cleanup, nil
		},
	)

	return activeWallet, func() { activeWallet.Close() }, err
}
