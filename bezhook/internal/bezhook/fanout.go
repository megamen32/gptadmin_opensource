package bezhook

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/fatedier/frp/client"
	v1 "github.com/fatedier/frp/pkg/config/v1"
	"github.com/fatedier/frp/client/proxy"
)

// ProxyName is the frp proxy name used on every gateway for this tunnel.
func ProxyName(sub string) string { return "bezhook-" + sub }

// BuildClientConfig constructs the frp client configuration for one gateway.
// It mirrors the exact shape of the generated /etc/gptadmin/frpc*.toml files:
// token auth, forced TLS with the fleet SNI, and an http-vhost proxy bound to
// the local port.
func BuildClientConfig(gw Gateway, opts Options) *v1.ClientConfig {
	loginFailExit := false
	tlsEnable := true
	proxyCfg := &v1.HTTPProxyConfig{
		DomainConfig: v1.DomainConfig{
			SubDomain: opts.Subdomain,
		},
	}
	proxyCfg.Name = ProxyName(opts.Subdomain)
	proxyCfg.Type = "http"
	proxyCfg.Transport.UseEncryption = true
	proxyCfg.Transport.UseCompression = true
	proxyCfg.LocalIP = opts.LocalHost
	proxyCfg.LocalPort = opts.LocalPort

	return &v1.ClientConfig{
		ClientCommonConfig: v1.ClientCommonConfig{
			Auth: v1.AuthClientConfig{
				Method: "token",
				Token:  opts.Token,
			},
			ServerAddr:    gw.Addr,
			ServerPort:    gw.Port,
			LoginFailExit: &loginFailExit,
			Log:           v1.LogConfig{Level: "warn"},
			Transport: v1.ClientTransportConfig{
				TLS: v1.TLSClientConfig{
					Enable:     &tlsEnable,
					TLSConfig:  v1.TLSConfig{ServerName: opts.TLSServerName},
				},
			},
		},
		Proxies: []v1.TypedProxyConfig{
			{Type: "http", ProxyConfigurer: proxyCfg},
		},
	}
}

// GatewayState is reported to the Status callback on every change.
type GatewayState struct {
	Gateway Gateway
	Online  bool
	Phase   string // frp proxy phase: "running", "start error", ...
	Err     string
}

// RunFanout connects to every gateway simultaneously and keeps each edge
// alive until ctx is done. It blocks until all gateway loops have stopped.
// Status changes are reported through cb (may be called from goroutines).
func RunFanout(ctx context.Context, opts Options, cb func(GatewayState)) error {
	if err := ValidateSubdomain(opts.Subdomain); err != nil {
		return err
	}
	if len(opts.Endpoints) == 0 {
		return fmt.Errorf("no gateways configured")
	}
	if opts.LocalPort < 1 || opts.LocalPort > 65535 {
		return fmt.Errorf("bad local port %d", opts.LocalPort)
	}

	var wg sync.WaitGroup
	for _, gw := range opts.Endpoints {
		gw := gw
		wg.Add(1)
		go func() {
			defer wg.Done()
			runGatewayLoop(ctx, gw, opts, cb)
		}()
	}
	wg.Wait()
	return nil
}

// runGatewayLoop runs one frp client Service against one gateway and watches
// its proxy phase. client.Service reconnects internally (LoginFailExit=false,
// keepControllerWorking), so the outer loop only guards against Run() itself
// returning, which happens on ctx cancel or a fatal construction error.
func runGatewayLoop(ctx context.Context, gw Gateway, opts Options, cb func(GatewayState)) {
	name := ProxyName(opts.Subdomain)
	for {
		cfg := BuildClientConfig(gw, opts)
		svc, err := client.NewService(client.ServiceOptions{
			Common:    &cfg.ClientCommonConfig,
			ProxyCfgs: []v1.ProxyConfigurer{cfg.Proxies[0].ProxyConfigurer},
		})
		if err != nil {
			cb(GatewayState{Gateway: gw, Online: false, Phase: "init error", Err: err.Error()})
			if !sleepCtx(ctx, 5*time.Second) {
				return
			}
			continue
		}

		runErr := make(chan error, 1)
		go func() { runErr <- svc.Run(ctx) }()

		watchProxyPhase(ctx, svc, name, gw, cb)

		// Wait for either Run to finish or ctx to be done.
		select {
		case err := <-runErr:
			if ctx.Err() != nil {
				svc.Close()
				cb(GatewayState{Gateway: gw, Online: false, Phase: "stopped"})
				return
			}
			cb(GatewayState{Gateway: gw, Online: false, Phase: "service exited", Err: errString(err)})
			svc.Close()
		case <-ctx.Done():
			svc.GracefulClose(3 * time.Second)
			<-runErr
			cb(GatewayState{Gateway: gw, Online: false, Phase: "stopped"})
			return
		}
		if !sleepCtx(ctx, 5*time.Second) {
			return
		}
	}
}

// watchProxyPhase polls the proxy status until it reaches a terminal phase
// for this connection attempt, reporting transitions through cb. It returns
// once the proxy is running (traffic can flow) or ctx is done; later
// disconnects are handled by the frp reconnect loop.
func watchProxyPhase(ctx context.Context, svc *client.Service, name string, gw Gateway, cb func(GatewayState)) {
	exp := svc.StatusExporter()
	tick := time.NewTicker(500 * time.Millisecond)
	defer tick.Stop()
	lastPhase := ""
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			st, ok := exp.GetProxyStatus(name)
			phase, errMsg := "", ""
			if ok {
				phase, errMsg = st.Phase, st.Err
			}
			if phase == lastPhase {
				continue
			}
			lastPhase = phase
			switch phase {
			case proxy.ProxyPhaseRunning:
				cb(GatewayState{Gateway: gw, Online: true, Phase: phase})
				return
			case proxy.ProxyPhaseStartErr, proxy.ProxyPhaseCheckFailed:
				cb(GatewayState{Gateway: gw, Online: false, Phase: phase, Err: errMsg})
				// frp retries registration after start error; keep watching
				// so a later success (e.g. a freed subdomain) is reported.
			case proxy.ProxyPhaseClosed:
				cb(GatewayState{Gateway: gw, Online: false, Phase: phase, Err: errMsg})
			}
		}
	}
}

func sleepCtx(ctx context.Context, d time.Duration) bool {
	select {
	case <-ctx.Done():
		return false
	case <-time.After(d):
		return true
	}
}

func errString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}
