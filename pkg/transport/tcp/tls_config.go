package tcp

import (
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"os"
)

// TLSConfig wraps mutual TLS options for host-to-host communication.
type TLSConfig struct {
	CertFile   string
	KeyFile    string
	CAFile     string
	ServerName string
}

// BuildServerTLS creates a *tls.Config for the server listener, enforcing mTLS if CAFile is provided.
func (cfg *TLSConfig) BuildServerTLS() (*tls.Config, error) {
	if cfg == nil || (cfg.CertFile == "" && cfg.KeyFile == "") {
		return nil, nil
	}

	cert, err := tls.LoadX509KeyPair(cfg.CertFile, cfg.KeyFile)
	if err != nil {
		return nil, fmt.Errorf("tcp: failed to load server keypair: %w", err)
	}

	tlsConf := &tls.Config{
		Certificates: []tls.Certificate{cert},
		MinVersion:   tls.VersionTLS13,
	}

	if cfg.CAFile != "" {
		caCert, err := os.ReadFile(cfg.CAFile)
		if err != nil {
			return nil, fmt.Errorf("tcp: failed to read CA cert: %w", err)
		}
		caPool := x509.NewCertPool()
		if !caPool.AppendCertsFromPEM(caCert) {
			return nil, fmt.Errorf("tcp: failed to parse CA cert from %s", cfg.CAFile)
		}
		tlsConf.ClientCAs = caPool
		tlsConf.ClientAuth = tls.RequireAndVerifyClientCert
	}

	return tlsConf, nil
}

// BuildClientTLS creates a *tls.Config for the client dialer, presenting cert if provided.
func (cfg *TLSConfig) BuildClientTLS() (*tls.Config, error) {
	if cfg == nil {
		return nil, nil
	}

	tlsConf := &tls.Config{
		ServerName: cfg.ServerName,
		MinVersion: tls.VersionTLS13,
	}

	if cfg.CertFile != "" && cfg.KeyFile != "" {
		cert, err := tls.LoadX509KeyPair(cfg.CertFile, cfg.KeyFile)
		if err != nil {
			return nil, fmt.Errorf("tcp: failed to load client keypair: %w", err)
		}
		tlsConf.Certificates = []tls.Certificate{cert}
	}

	if cfg.CAFile != "" {
		caCert, err := os.ReadFile(cfg.CAFile)
		if err != nil {
			return nil, fmt.Errorf("tcp: failed to read CA cert: %w", err)
		}
		caPool := x509.NewCertPool()
		if !caPool.AppendCertsFromPEM(caCert) {
			return nil, fmt.Errorf("tcp: failed to parse CA cert from %s", cfg.CAFile)
		}
		tlsConf.RootCAs = caPool
	}

	return tlsConf, nil
}
