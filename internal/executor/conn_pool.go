package executor

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/go-kratos/kratos/v2/log"
	"google.golang.org/grpc"
	"google.golang.org/grpc/connectivity"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"

	"github.com/go-tangra/go-tangra-common/grpcx"
)

// ModuleConnPool caches gRPC connections per module_id to avoid
// repeated ResolveModule + TLS handshake on every task execution.
type ModuleConnPool struct {
	mu     sync.RWMutex
	conns  map[string]*grpc.ClientConn
	dialer *grpcx.ModuleDialer
	log    *log.Helper
}

func NewModuleConnPool(dialer *grpcx.ModuleDialer, log *log.Helper) *ModuleConnPool {
	return &ModuleConnPool{
		conns:  make(map[string]*grpc.ClientConn),
		dialer: dialer,
		log:    log,
	}
}

// Get returns a cached or newly dialed connection for a module.
func (p *ModuleConnPool) Get(ctx context.Context, moduleID string) (*grpc.ClientConn, error) {
	p.mu.RLock()
	if conn, ok := p.conns[moduleID]; ok {
		state := conn.GetState()
		if state != connectivity.Shutdown {
			p.mu.RUnlock()
			return conn, nil
		}
	}
	p.mu.RUnlock()

	p.log.Infof("Dialing module %s for task execution", moduleID)

	conn, err := p.dialModule(ctx, moduleID)
	if err != nil {
		return nil, err
	}

	p.mu.Lock()
	p.conns[moduleID] = conn
	p.mu.Unlock()

	return conn, nil
}

// dialModule tries ModuleDialer first, falls back to env var {MODULE_ID}_GRPC_ENDPOINT.
func (p *ModuleConnPool) dialModule(ctx context.Context, moduleID string) (*grpc.ClientConn, error) {
	// Try ModuleDialer first (uses admin-service ResolveModule)
	if p.dialer != nil {
		conn, err := p.dialer.DialModule(ctx, moduleID, 2, 5*time.Second)
		if err == nil {
			return conn, nil
		}
		p.log.Warnf("ModuleDialer failed for %s: %v, trying env var fallback", moduleID, err)
	}

	// Fallback: check {MODULE_ID}_GRPC_ENDPOINT env var
	envKey := strings.ToUpper(moduleID) + "_GRPC_ENDPOINT"
	endpoint := os.Getenv(envKey)
	if endpoint == "" {
		return nil, fmt.Errorf("no endpoint for module %s (ModuleDialer failed and %s not set)", moduleID, envKey)
	}

	p.log.Infof("Using env var %s=%s for module %s", envKey, endpoint, moduleID)

	transportCreds := p.loadClientTLS(moduleID)
	conn, err := grpc.NewClient(endpoint, transportCreds)
	if err != nil {
		return nil, fmt.Errorf("dial module %s at %s: %w", moduleID, endpoint, err)
	}
	return conn, nil
}

// loadClientTLS tries to load mTLS credentials for calling a module.
func (p *ModuleConnPool) loadClientTLS(moduleID string) grpc.DialOption {
	certsDir := os.Getenv("CERTS_DIR")
	if certsDir == "" {
		// Check SCHEDULER server certs parent dir
		caCertPath := os.Getenv("SCHEDULER_CA_CERT_PATH")
		if caCertPath != "" {
			certsDir = filepath.Dir(filepath.Dir(caCertPath))
		}
	}
	if certsDir == "" {
		return grpc.WithTransportCredentials(insecure.NewCredentials())
	}

	caCert, err := os.ReadFile(filepath.Join(certsDir, "ca", "ca.crt"))
	if err != nil {
		return grpc.WithTransportCredentials(insecure.NewCredentials())
	}

	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(caCert) {
		return grpc.WithTransportCredentials(insecure.NewCredentials())
	}

	// Look for scheduler/scheduler.crt or admin/admin.crt as client cert
	for _, name := range []string{"scheduler", "admin"} {
		certPath := filepath.Join(certsDir, name, name+".crt")
		keyPath := filepath.Join(certsDir, name, name+".key")
		cert, err := tls.LoadX509KeyPair(certPath, keyPath)
		if err == nil {
			p.log.Infof("Using mTLS client cert %s for module %s", certPath, moduleID)
			return grpc.WithTransportCredentials(credentials.NewTLS(&tls.Config{
				Certificates: []tls.Certificate{cert},
				RootCAs:      pool,
				ServerName:   moduleID + "-service",
				MinVersion:   tls.VersionTLS12,
			}))
		}
	}

	return grpc.WithTransportCredentials(insecure.NewCredentials())
}

// Close closes all cached connections.
func (p *ModuleConnPool) Close() {
	p.mu.Lock()
	defer p.mu.Unlock()
	for id, conn := range p.conns {
		if err := conn.Close(); err != nil {
			p.log.Warnf("failed to close connection to module %s: %v", id, err)
		}
	}
	p.conns = make(map[string]*grpc.ClientConn)
}
