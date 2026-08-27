package main

import (
	"context"
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
	"github.com/bsv-blockchain/go-wallet-toolbox/pkg/services"
	"github.com/bsv-blockchain/go-wallet-toolbox/pkg/storage"
	"github.com/bsv-blockchain/go-wallet-toolbox/pkg/wallet"
	"github.com/bsv-blockchain/go-wallet-toolbox/pkg/wdk"
)

// Here we define a hardcoded XPriv for the example so no config file is needed.
const exampleXPriv = "xprv9s21ZrQH143K3WYAquX13GWNfPShBx5XT98kBDMQpxz5p1EYJ8fsqwQCkKuJyB7"

//go:embed static/*
var staticFS embed.FS

func setupWallet(ctx context.Context) (*wallet.Wallet, func(), error) {
	logger := slog.New(slog.NewTextHandler(os.Stdout, nil))

	// Define private key for the wallet
	aliceKey, _ := ec.PrivateKeyFromBytes([]byte(exampleXPriv))
	if aliceKey == nil {
		return nil, nil, fmt.Errorf("cannot create Alice private key")
	}

	// Configure the default ARC Broadcaster / Network Services
	cfg := infra.Defaults()
	cfg.BSVNetwork = defs.NetworkMainnet
	cfg.Services = defs.DefaultServicesConfig(defs.NetworkMainnet)
	cfg.DBConfig.SQLite.ConnectionString = "news-server.sqlite"

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
			if _, err := store.Migrate(ctx, "news_example_wallet", storeIdentityKey); err != nil {
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

func main() {
	ctx := context.Background()

	// Enable debug logging so payment validation steps are visible.
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelDebug})))

	fmt.Println("Initializing real wallet with SQLite storage and ARC broadcaster...")
	realWallet, cleanup, err := setupWallet(ctx)
	if err != nil {
		panic(fmt.Errorf("failed to setup wallet: %w", err))
	}
	defer cleanup()

	idKey, _ := realWallet.GetPublicKey(ctx, sdk.GetPublicKeyArgs{IdentityKey: true}, "")
	fmt.Printf("News Server Wallet Identity Key initialized: %s\n", idKey.PublicKey.ToDERHex())

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

	// Wrap the mux with the 402 Payment Middleware
	paymentMiddleware := pay402.PaymentMiddleware(pay402.MiddlewareOptions{
		Wallet: realWallet,
		CalculatePrice: func(path string) int {
			if path == "/article/premium" {
				return 100
			}
			return 0
		},
		Logger: slog.New(slog.NewTextHandler(os.Stdout, nil)),
	}, mux)

	fmt.Println("News Server listening on http://localhost:8080")
	if err := http.ListenAndServe(":8080", paymentMiddleware); err != nil {
		panic(err)
	}
}
