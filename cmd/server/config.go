package main

import (
	"errors"
	"fmt"
	"os"
	"regexp"
	"strconv"
	"time"

	"github.com/hyperledger/fabric-x-sdk/network"
	"gopkg.in/yaml.v3"

	"x-client/pkg/contract"
	"x-client/pkg/endorser"
	"x-client/pkg/models"
)

// TLSConfig mirrors network.TLSConfig in YAML. Mode is "none", "tls" or "mtls".
type TLSConfig struct {
	Mode        string   `yaml:"mode"`
	CACertPaths []string `yaml:"ca_cert_paths"`
	CertPath    string   `yaml:"cert_path"`
	KeyPath     string   `yaml:"key_path"`
	ServerName  string   `yaml:"server_name"`
}

func (t TLSConfig) toSDK() network.TLSConfig {
	return network.TLSConfig{
		Mode:        t.Mode,
		CACertPaths: t.CACertPaths,
		CertPath:    t.CertPath,
		KeyPath:     t.KeyPath,
		ServerName:  t.ServerName,
	}
}

// Endpoint is a gRPC endpoint of the Fabric-X network.
type Endpoint struct {
	Endpoint string    `yaml:"endpoint"`
	TLS      TLSConfig `yaml:"tls"`
}

// PeerConfig is another organization's X-Client.
type PeerConfig struct {
	Endpoint string `yaml:"endpoint"` // base URL, e.g. http://x-client-centralbank:8080
	MSPCA    string `yaml:"msp_ca"`   // that organization's MSP CA certificate
}

type Config struct {
	Server struct {
		Port     int    `yaml:"port"`
		Host     string `yaml:"host"`
		APIToken string `yaml:"api_token"`
	} `yaml:"server"`

	// Mode is "grpc" (live Fabric-X network) or "in-memory" (standalone ledger).
	Mode      string `yaml:"mode"`
	ChannelID string `yaml:"channel_id"`

	// CentralBankMSP is the issuer: it creates wallets, mints and co-signs every transaction.
	CentralBankMSP string `yaml:"central_bank_msp"`
	// DefaultMSPID is the organization this instance acts as (defaults to the issuer).
	DefaultMSPID string `yaml:"default_msp_id"`
	// AllowCallerHeader lets requests choose the acting MSP with X-Caller-MSP. Only for a
	// single trusted process holding several organizations' keys.
	AllowCallerHeader bool `yaml:"allow_caller_header"`
	// Endorsers are the identities this process holds keys for.
	Endorsers map[string]endorser.Config `yaml:"endorsers"`

	QueryService  Endpoint      `yaml:"query_service"`
	Sidecar       Endpoint      `yaml:"sidecar"`
	Orderers      []Endpoint    `yaml:"orderers"`
	CommitTimeout time.Duration `yaml:"commit_timeout"`

	// Peers are the X-Clients of the other organizations, keyed by MSP ID.
	Peers map[string]PeerConfig `yaml:"peers"`

	Namespaces struct {
		Public         string            `yaml:"public"`
		CentralBankPDC string            `yaml:"central_bank_pdc"`
		Credits        string            `yaml:"credits"`
		Banks          map[string]string `yaml:"banks"` // MSP ID -> balance namespace
	} `yaml:"namespaces"`

	// EndorsementPolicies overrides the default namespace -> required MSPs mapping.
	EndorsementPolicies map[string][]string `yaml:"endorsement_policies"`
}

func loadConfig(path string) (*Config, error) {
	cfg := &Config{}
	cfg.Server.Port = 8080
	cfg.Server.Host = "0.0.0.0"
	cfg.Mode = "in-memory"
	cfg.ChannelID = "mychannel"
	cfg.CentralBankMSP = models.CentralBankMSP
	cfg.CommitTimeout = 30 * time.Second

	data, err := os.ReadFile(path)
	switch {
	case errors.Is(err, os.ErrNotExist):
		// Defaults plus environment overrides.
	case err != nil:
		return nil, err
	default:
		if err := yaml.Unmarshal([]byte(expandEnv(string(data))), cfg); err != nil {
			return nil, fmt.Errorf("parse config: %w", err)
		}
	}

	if p := os.Getenv("PORT"); p != "" {
		port, err := strconv.Atoi(p)
		if err != nil {
			return nil, fmt.Errorf("PORT: %w", err)
		}
		cfg.Server.Port = port
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
	if tok := os.Getenv("API_TOKEN"); tok != "" {
		cfg.Server.APIToken = tok
	}
	if cfg.DefaultMSPID == "" {
		cfg.DefaultMSPID = cfg.CentralBankMSP
	}
	return cfg, cfg.validate()
}

var envRef = regexp.MustCompile(`\$\{([A-Za-z_][A-Za-z0-9_]*)(?::-([^}]*))?\}`)

// expandEnv replaces ${VAR} and ${VAR:-default} so one config file serves both host
// and Docker deployments.
func expandEnv(s string) string {
	return envRef.ReplaceAllStringFunc(s, func(ref string) string {
		m := envRef.FindStringSubmatch(ref)
		if v, ok := os.LookupEnv(m[1]); ok && v != "" {
			return v
		}
		return m[2]
	})
}

func (c *Config) validate() error {
	switch c.Mode {
	case "in-memory":
	case "grpc":
		if c.QueryService.Endpoint == "" || c.Sidecar.Endpoint == "" || len(c.Orderers) == 0 {
			return errors.New("grpc mode requires query_service, sidecar and orderers")
		}
		if _, ok := c.Endorsers[c.DefaultMSPID]; !ok {
			return fmt.Errorf("grpc mode requires a key for default_msp_id %s under endorsers", c.DefaultMSPID)
		}
	default:
		return fmt.Errorf(`mode must be "grpc" or "in-memory", got %q`, c.Mode)
	}
	return nil
}

// namespaces returns the contract's namespace layout, filling in defaults.
func (c *Config) namespaces() contract.Namespaces {
	ns := contract.DefaultNamespaces()
	ns.CentralBankMSP = c.CentralBankMSP
	if c.Namespaces.Public != "" {
		ns.Public = c.Namespaces.Public
	}
	if c.Namespaces.CentralBankPDC != "" {
		ns.Supply = c.Namespaces.CentralBankPDC
	}
	if c.Namespaces.Credits != "" {
		ns.Credits = c.Namespaces.Credits
	}
	ns.Banks = c.Namespaces.Banks
	if len(ns.Banks) == 0 {
		ns.Banks = map[string]string{
			"Org1MSP": models.PDCNameForMSP("Org1MSP"),
			"Org2MSP": models.PDCNameForMSP("Org2MSP"),
		}
	}
	return ns
}
