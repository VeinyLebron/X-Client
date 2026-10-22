package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"maps"
	"net/http"
	"os"
	"os/signal"
	"slices"
	"syscall"
	"time"

	sdk "github.com/hyperledger/fabric-x-sdk"
	"github.com/hyperledger/fabric-x-sdk/network"
	nfabx "github.com/hyperledger/fabric-x-sdk/network/fabricx"

	"x-client/pkg/api"
	"x-client/pkg/contract"
	"x-client/pkg/endorser"
	"x-client/pkg/engine"
	"x-client/pkg/fabricx"
	"x-client/pkg/models"
	"x-client/pkg/peers"
	"x-client/pkg/store"
)

func main() {
	configPath := flag.String("config", "config/config.yaml", "path to configuration file")
	flag.Parse()

	cfg, err := loadConfig(*configPath)
	if err != nil {
		log.Fatalf("failed to load configuration: %v", err)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := run(ctx, cfg); err != nil {
		log.Fatal(err)
	}
}

func run(ctx context.Context, cfg *Config) error {
	svc := contract.NewCBDCService(cfg.namespaces())
	policies := engine.DefaultPolicies(svc, slices.Sorted(maps.Keys(cfg.namespaces().Banks)))
	maps.Copy(policies, cfg.EndorsementPolicies)

	registry, err := loadIdentities(cfg, policies)
	if err != nil {
		return err
	}

	var (
		st        store.Store
		submitter fabricx.Submitter
		ready     func() error
	)
	switch cfg.Mode {
	case "grpc":
		identity, _ := registry.Get(cfg.DefaultMSPID)
		qs, sub, sync, err := connectNetwork(ctx, cfg, identity)
		if err != nil {
			return err
		}
		defer qs.Close()
		defer sub.Close()
		st, submitter, ready = qs, sub, sync.Ready
		go func() {
			if err := sync.Start(ctx); err != nil {
				log.Printf("[Sync] stopped: %v", err)
			}
		}()
	default:
		log.Printf("[Storage] in-memory ledger: transactions commit locally, nothing reaches a Fabric-X network")
		mem := store.NewInMemoryStore()
		st, submitter = mem, fabricx.LocalSubmitter{Store: mem}
	}

	peerClient, verifier, err := setupPeers(cfg)
	if err != nil {
		return err
	}

	eng := engine.New(engine.Config{
		Contract:  svc,
		Store:     st,
		Registry:  registry,
		Policies:  policies,
		Peers:     peerClient,
		Submitter: submitter,
		Channel:   cfg.ChannelID,
	})
	server := api.NewServer(svc, eng, api.Options{
		DefaultMSP:        cfg.DefaultMSPID,
		AllowCallerHeader: cfg.AllowCallerHeader,
		APIToken:          cfg.Server.APIToken,
		Verifier:          verifier,
		Ready:             ready,
		Mode:              cfg.Mode,
	})

	addr := fmt.Sprintf("%s:%d", cfg.Server.Host, cfg.Server.Port)
	httpServer := &http.Server{
		Addr:              addr,
		Handler:           server.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
	}
	logStartup(cfg, registry, policies)

	errCh := make(chan error, 1)
	go func() {
		if err := httpServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
	}()

	select {
	case err := <-errCh:
		return fmt.Errorf("server error: %w", err)
	case <-ctx.Done():
	}
	log.Println("Shutting down server gracefully...")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return httpServer.Shutdown(shutdownCtx)
}

// loadIdentities loads the configured signing keys. In in-memory mode, MSPs without key
// files get throwaway identities so the full endorsement flow can run standalone.
func loadIdentities(cfg *Config, policies engine.Policies) (*endorser.Registry, error) {
	registry := endorser.NewRegistry(cfg.DefaultMSPID)
	for mspID, endCfg := range cfg.Endorsers {
		endCfg.MSPID = mspID
		if cfg.Mode == "in-memory" && endCfg.CertPath == "" && endCfg.KeyPath == "" {
			continue
		}
		id, err := endorser.Load(endCfg)
		if err != nil {
			return nil, fmt.Errorf("load identity: %w", err)
		}
		registry.Register(id)
	}

	if cfg.Mode == "in-memory" {
		for _, mspID := range append(policies.MSPs(), cfg.DefaultMSPID) {
			if _, ok := registry.Get(mspID); ok {
				continue
			}
			id, err := endorser.Generate(mspID)
			if err != nil {
				return nil, err
			}
			registry.Register(id)
			log.Printf("[Endorser] %s: generated ephemeral identity (in-memory mode only)", mspID)
		}
	}
	return registry, nil
}

// connectNetwork dials the Query Service, the Arma routers and the sidecar block stream.
func connectNetwork(ctx context.Context, cfg *Config, identity *endorser.Identity) (*store.GRPCQueryServiceStore, *fabricx.NetworkSubmitter, *network.Synchronizer, error) {
	index := store.NewKeyIndex()
	qs, err := store.NewGRPCQueryServiceStore(store.GRPCConfig{
		Endpoint: cfg.QueryService.Endpoint,
		TLS:      cfg.QueryService.TLS.toSDK(),
	}, index)
	if err != nil {
		return nil, nil, nil, err
	}

	sync, err := nfabx.NewSynchronizer(index, cfg.ChannelID, network.PeerConf{
		Address: cfg.Sidecar.Endpoint,
		TLS:     cfg.Sidecar.TLS.toSDK(),
	}, identity, sdk.NewStdLogger("Sync"), index)
	if err != nil {
		qs.Close()
		return nil, nil, nil, fmt.Errorf("sidecar: %w", err)
	}

	orderers := make([]network.OrdererConf, len(cfg.Orderers))
	for i, o := range cfg.Orderers {
		orderers[i] = network.OrdererConf{Address: o.Endpoint, TLS: o.TLS.toSDK()}
	}
	sub, err := fabricx.NewNetworkSubmitter(ctx, orderers, qs.Client(), cfg.CommitTimeout)
	if err != nil {
		qs.Close()
		return nil, nil, nil, fmt.Errorf("orderers: %w", err)
	}
	log.Printf("[Network] query service %s, sidecar %s, %d router(s), channel %s",
		cfg.QueryService.Endpoint, cfg.Sidecar.Endpoint, len(orderers), cfg.ChannelID)
	return qs, sub, sync, nil
}

// setupPeers builds the client for other organizations' X-Clients and the verifier
// for their requests to this one.
func setupPeers(cfg *Config) (*peers.Client, *peers.Verifier, error) {
	if len(cfg.Peers) == 0 {
		return nil, nil, nil
	}
	endpoints := make(map[string]string, len(cfg.Peers))
	cas := make(map[string]string, len(cfg.Peers))
	for msp, p := range cfg.Peers {
		if p.Endpoint == "" || p.MSPCA == "" {
			return nil, nil, fmt.Errorf("peer %s: endpoint and msp_ca are required", msp)
		}
		endpoints[msp] = p.Endpoint
		cas[msp] = p.MSPCA
	}
	verifier, err := peers.NewVerifier(cas)
	if err != nil {
		return nil, nil, err
	}
	return peers.NewClient(endpoints, nil), verifier, nil
}

func logStartup(cfg *Config, registry *endorser.Registry, policies engine.Policies) {
	log.Println("=================================================================")
	log.Printf(" Fabric-X Endorser REST Client for Wholesale CBDC")
	log.Println("=================================================================")
	log.Printf(" REST API            : http://%s:%d", cfg.Server.Host, cfg.Server.Port)
	log.Printf(" Mode                : %s (channel %s)", cfg.Mode, cfg.ChannelID)
	log.Printf(" Acting as           : %s", cfg.DefaultMSPID)
	log.Printf(" Local signing keys  : %v", slices.Sorted(slices.Values(registry.MSPIDs())))
	for _, ns := range slices.Sorted(maps.Keys(policies)) {
		log.Printf(" Policy %-14s: AND%v", ns, policies[ns])
	}
	for _, msp := range slices.Sorted(maps.Keys(cfg.Peers)) {
		log.Printf(" Peer %-16s: %s", msp, cfg.Peers[msp].Endpoint)
	}
	if cfg.AllowCallerHeader {
		log.Printf(" WARNING: X-Caller-MSP header enabled; any API client can act as any local MSP")
	}
	if cfg.Server.APIToken == "" && cfg.Mode == "grpc" {
		log.Printf(" WARNING: no api_token set; restrict network access to the REST API")
	}
	if cfg.DefaultMSPID == models.CentralBankMSP && cfg.Mode == "grpc" && len(cfg.Peers) == 0 && registry.Len() == 1 {
		log.Printf(" NOTE: no peers configured; bank-owned namespaces cannot be endorsed")
	}
	log.Println("=================================================================")
}
