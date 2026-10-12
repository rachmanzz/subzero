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

	// In-memory PEM bytes (takes priority if provided)
	CertPEM []byte
	KeyPEM  []byte
	CAPEM   []byte
}

// BuildServerTLS creates a *tls.Config for the server listener, enforcing mTLS if CAFile or CAPEM is provided.
func (cfg *TLSConfig) BuildServerTLS() (*tls.Config, error) {
	if cfg == nil {
		return nil, nil
	}
	if len(cfg.CertPEM) == 0 && len(cfg.KeyPEM) == 0 && cfg.CertFile == "" && cfg.KeyFile == "" {
		return nil, nil
	}

	var cert tls.Certificate
	var err error
	if len(cfg.CertPEM) > 0 && len(cfg.KeyPEM) > 0 {
		cert, err = tls.X509KeyPair(cfg.CertPEM, cfg.KeyPEM)
	} else {
		cert, err = tls.LoadX509KeyPair(cfg.CertFile, cfg.KeyFile)
	}
	if err != nil {
		return nil, fmt.Errorf("tcp: failed to load server keypair: %w", err)
	}

	tlsConf := &tls.Config{
		Certificates: []tls.Certificate{cert},
		MinVersion:   tls.VersionTLS13,
	}

	if len(cfg.CAPEM) > 0 {
		caPool := x509.NewCertPool()
		if !caPool.AppendCertsFromPEM(cfg.CAPEM) {
			return nil, fmt.Errorf("tcp: failed to parse CA cert from CAPEM")
		}
		tlsConf.ClientCAs = caPool
		tlsConf.ClientAuth = tls.RequireAndVerifyClientCert
	} else if cfg.CAFile != "" {
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

	if len(cfg.CertPEM) > 0 && len(cfg.KeyPEM) > 0 {
		cert, err := tls.X509KeyPair(cfg.CertPEM, cfg.KeyPEM)
		if err != nil {
			return nil, fmt.Errorf("tcp: failed to load client keypair from PEM: %w", err)
		}
		tlsConf.Certificates = []tls.Certificate{cert}
	} else if cfg.CertFile != "" && cfg.KeyFile != "" {
		cert, err := tls.LoadX509KeyPair(cfg.CertFile, cfg.KeyFile)
		if err != nil {
			return nil, fmt.Errorf("tcp: failed to load client keypair: %w", err)
		}
		tlsConf.Certificates = []tls.Certificate{cert}
	}

	if len(cfg.CAPEM) > 0 {
		caPool := x509.NewCertPool()
		if !caPool.AppendCertsFromPEM(cfg.CAPEM) {
			return nil, fmt.Errorf("tcp: failed to parse CA cert from CAPEM")
		}
		tlsConf.RootCAs = caPool
	} else if cfg.CAFile != "" {
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
