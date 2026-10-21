package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"gopkg.in/yaml.v3"

	"x-client/pkg/api"
	"x-client/pkg/contract"
	"x-client/pkg/endorser"
	"x-client/pkg/models"
	"x-client/pkg/store"
)

type Config struct {
	Server struct {
		Port int    `yaml:"port"`
		Host string `yaml:"host"`
	} `yaml:"server"`
	Mode         string `yaml:"mode"` // "grpc" or "in-memory"
	QueryService struct {
		Endpoint string `yaml:"endpoint"`
		TLS      struct {
			Enabled    bool   `yaml:"enabled"`
			CACert     string `yaml:"ca_cert"`
			ClientCert string `yaml:"client_cert"`
			ClientKey  string `yaml:"client_key"`
		} `yaml:"tls"`
	} `yaml:"query_service"`
	DefaultMSPID string                     `yaml:"default_msp_id"`
	Endorsers    map[string]endorser.Config `yaml:"endorsers"`
}

func loadConfig(path string) (*Config, error) {
	cfg := &Config{
		DefaultMSPID: models.CentralBankMSP,
		Endorsers:    make(map[string]endorser.Config),
	}
	cfg.Server.Port = 8080
	cfg.Server.Host = "0.0.0.0"
	cfg.Mode = "in-memory"
	cfg.QueryService.Endpoint = "localhost:7001"

	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return cfg, nil
		}
		return nil, err
	}

	if err := yaml.Unmarshal(data, cfg); err != nil {
		return nil, fmt.Errorf("parse config: %w", err)
	}

	// Environment variable overrides
	if p := os.Getenv("PORT"); p != "" {
		fmt.Sscanf(p, "%d", &cfg.Server.Port)
	}
	if ep := os.Getenv("QUERY_SERVICE_ENDPOINT"); ep != "" {
		cfg.QueryService.Endpoint = ep
	}
	if msp := os.Getenv("DEFAULT_MSP_ID"); msp != "" {
		cfg.DefaultMSPID = msp
	}
	if mode := os.Getenv("MODE"); mode != "" {
		cfg.Mode = mode
	}

	return cfg, nil
}

func main() {
	configPath := flag.String("config", "config/config.yaml", "path to configuration file")
	flag.Parse()

	cfg, err := loadConfig(*configPath)
	if err != nil {
		log.Fatalf("failed to load configuration: %v", err)
	}

	// Initialize Store
	var st store.Store
	if cfg.Mode == "grpc" {
		log.Printf("[Storage] Connecting to Fabric-X Query Service at %s (TLS: %v)...",
			cfg.QueryService.Endpoint, cfg.QueryService.TLS.Enabled)

		grpcStore, err := store.NewGRPCQueryServiceStore(store.GRPCConfig{
			Endpoint:   cfg.QueryService.Endpoint,
			TLSEnabled: cfg.QueryService.TLS.Enabled,
			CACertPath: cfg.QueryService.TLS.CACert,
			ClientCert: cfg.QueryService.TLS.ClientCert,
			ClientKey:  cfg.QueryService.TLS.ClientKey,
		})
		if err != nil {
			log.Fatalf("failed to connect to Query Service: %v", err)
		}
		st = grpcStore
		defer grpcStore.Close()
	} else {
		log.Printf("[Storage] Running in 'in-memory' standalone mode (no external Query Service required)")
		st = store.NewInMemoryStore()
	}

	// Initialize Endorser Registry (Multi-org support)
	registry := endorser.NewRegistry(cfg.DefaultMSPID)
	for mspID, endCfg := range cfg.Endorsers {
		endCfg.MSPID = mspID
		end, err := endorser.New(endCfg)
		if err != nil {
			log.Fatalf("failed to initialize endorser %s: %v", mspID, err)
		}
		registry.Register(end)
		log.Printf("[Endorser] Registered identity for MSP: %s", mspID)
	}

	// Initialize Contract Service & REST API Server
	cbdcSvc := contract.NewCBDCService()
	server := api.NewServer(cbdcSvc, st, registry)

	addr := fmt.Sprintf("%s:%d", cfg.Server.Host, cfg.Server.Port)
	httpServer := &http.Server{
		Addr:    addr,
		Handler: server.Handler(),
	}

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)

	go func() {
		log.Println("=================================================================")
		log.Printf(" Fabric-X Endorser REST Client for Wholesale CBDC")
		log.Println("=================================================================")
		log.Printf(" REST API Port       : http://localhost:%d", cfg.Server.Port)
		log.Printf(" Query Service Port  : %s (Mode: %s)", cfg.QueryService.Endpoint, cfg.Mode)
		log.Printf(" Default Endorser MSP: %s", registry.DefaultMSPID())
		log.Println(" Configured MSPs     : CentralBankMSP, Org1MSP, Org2MSP")
		log.Println(" Available Endpoints :")
		log.Println("   POST /api/v1/wallets               - Create wholesale wallet (CentralBank)")
		log.Println("   POST /api/v1/wallets/:id/freeze    - Freeze wallet (CentralBank)")
		log.Println("   POST /api/v1/wallets/:id/unfreeze  - Unfreeze wallet (CentralBank)")
		log.Println("   GET  /api/v1/wallets/:id           - Query wallet public metadata")
		log.Println("   GET  /api/v1/wallets/:id/balance   - Query confidential balance (Owner only)")
		log.Println("   POST /api/v1/wallets/:id/consolidate - Consolidate pending credits (Owner only)")
		log.Println("   POST /api/v1/movements/mint        - Mint CBDC (CentralBank only)")
		log.Println("   POST /api/v1/movements/transfer    - Transfer CBDC (Interbank settlement)")
		log.Println("   POST /api/v1/movements/redeem      - Redeem CBDC (Burn from circulation)")
		log.Println("   GET  /api/v1/movements/:id         - Query public movement record")
		log.Println("   POST /api/v1/movements/:id/verify  - Verify movement amount commitment")
		log.Println("   GET  /api/v1/supply                - Query total supply in circulation (CentralBank)")
		log.Println("   GET  /healthz                      - Health & endorser status")
		log.Println("=================================================================")

		if err := httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("server error: %v", err)
		}
	}()

	<-stop
	log.Println("Shutting down server gracefully...")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := httpServer.Shutdown(ctx); err != nil {
		log.Printf("shutdown error: %v", err)
	}
	log.Println("Server stopped.")
}
