// bezhook: one-command public tunnel for a local port.
//
//	bezhook 3000                 -> https://your-subdomain.t.gptadmin.example
//	bezhook 3000 --sub myapp     -> https://myapp.t.became.bezrabotnyi.com
//
// Connects to every fleet gateway (server-100, server01, server01) at once, so the
// tunnel keeps working while any single gateway is alive. Default lease TTL
// is 72h of process lifetime; --ttl 0 disables the limit.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"sync"
	"syscall"

	"github.com/megamen32/gptadmin/bezhook/internal/bezhook"
	frplog "github.com/fatedier/frp/pkg/util/log"
)

var version = "0.2.0"

func usage(fs *flag.FlagSet) {
	fmt.Fprintf(os.Stderr, "bezhook v%s — self-hosted tunnel: expose a local port as https://<sub>.%s\n\n", version, bezhook.DefaultDomain)
	fmt.Fprintf(os.Stderr, "Usage: bezhook [flags] <local-port>\n\n")
	fs.PrintDefaults()
	fmt.Fprintf(os.Stderr, "\nExamples:\n")
	fmt.Fprintf(os.Stderr, "  bezhook 3000                 random subdomain, 72h ttl\n")
	fmt.Fprintf(os.Stderr, "  bezhook 3000 --sub myapp     fixed subdomain\n")
	fmt.Fprintf(os.Stderr, "  bezhook 3000 --ttl 0         no auto-stop\n")
	fmt.Fprintf(os.Stderr, "  bezhook 8080 --endpoints server01.bezrabotnyi.com:27000\n")
}

func main() {
	fs := flag.NewFlagSet("bezhook", flag.ExitOnError)
	fs.Usage = func() { usage(fs) }
	var (
		port      = fs.Int("port", 0, "local port to expose (or give it positionally)")
		sub       = fs.String("sub", "", "subdomain (default: random u-fNNNNNNN)")
		host      = fs.String("host", "127.0.0.1", "local host to forward to")
		ttl       = fs.String("ttl", "", "max lifetime, e.g. 72h/30m; 0 = unlimited (default 72h)")
		backend   = fs.String("backend", "frp", "primary tunnel provider: frp | cloudflared | ngrok")
		also      = fs.String("also", "", "extra providers to run simultaneously, e.g. cloudflared,ngrok")
		endpoints = fs.String("endpoints", "", "comma-separated host:port gateway list (overrides config/env)")
		token     = fs.String("token", "", "fleet auth token (overrides config/env)")
		domain    = fs.String("domain", "", "public domain suffix (overrides config/env)")
		config    = fs.String("config", "", fmt.Sprintf("config file (default %s, optional)", bezhook.DefaultConfigPath()))
		quiet     = fs.Bool("quiet", false, "print only the public URL")
		showVer   = fs.Bool("version", false, "print version and exit")
	)
	_ = fs.Parse(reorderLeadingPort(os.Args[1:]))

	if *showVer {
		fmt.Println("bezhook v" + version)
		return
	}

	// Quiet the frp library's info chatter; bezhook prints its own status lines.
	frplog.InitLogger("", "warn", 3, true)

	if *port == 0 && fs.NArg() > 0 {
		fmt.Sscanf(fs.Arg(0), "%d", port)
	}
	if *port == 0 {
		usage(fs)
		os.Exit(2)
	}

	fc, err := bezhook.LoadFileConfig(firstNonEmpty(*config, bezhook.DefaultConfigPath()))
	if err != nil {
		fatal(err)
	}

	// Provider selection: primary backend plus optional extras.
	primaryList, err := bezhook.ParseProviders(*backend)
	if err != nil {
		fatal(err)
	}
	primary := primaryList[0]
	var extras []string
	if strings.TrimSpace(*also) != "" {
		if extras, err = bezhook.ParseProviders(*also); err != nil {
			fatal(err)
		}
	}
	var providers []string
	for _, p := range append([]string{primary}, extras...) {
		dup := false
		for _, q := range providers {
			if p == q {
				dup = true
			}
		}
		if !dup {
			providers = append(providers, p)
		}
	}
	useFRP := false
	for _, p := range providers {
		if p == "frp" {
			useFRP = true
		}
	}
	if *sub != "" && !useFRP {
		fmt.Fprintln(os.Stderr, "bezhook: note: --sub работает только на frp-флоте; cloudflared/ngrok выдают случайный URL")
	}
	// Pre-flight external binaries; a missing provider is skipped with a hint
	// (and fatal when it is the only one).
	for _, p := range providers {
		if p == "frp" {
			continue
		}
		if bin, hint := bezhook.FindBin(p, bezhook.ExtraBinDirs()); bin == "" {
			fmt.Fprintf(os.Stderr, "bezhook: provider %s недоступен: %s\n", p, hint)
			providers = sliceRemove(providers, p)
		}
	}
	if len(providers) == 0 {
		os.Exit(1)
	}

	fleetToken, fleetDomain, tlsName, fleetEndpoints, err := bezhook.FleetSettings(fc, bezhook.Env(osEnvironCompact()))
	if err != nil {
		fatal(err)
	}
	if *token != "" {
		fleetToken = *token
	}
	if *domain != "" {
		fleetDomain = *domain
	}
	if *endpoints != "" {
		if fleetEndpoints, err = bezhook.ParseEndpoints(*endpoints); err != nil {
			fatal(err)
		}
	}

	resolvedTTL := bezhook.FileDefaultTTL(fc)
	if *ttl != "" {
		if resolvedTTL, err = bezhook.ParseTTL(*ttl); err != nil {
			fatal(err)
		}
	}

	subdomain := *sub
	if subdomain == "" {
		if subdomain, err = bezhook.RandomSubdomain(); err != nil {
			fatal(err)
		}
	}
	if err := bezhook.ValidateSubdomain(subdomain); err != nil {
		fatal(err)
	}

	opts := bezhook.Options{
		LocalHost:     *host,
		LocalPort:     *port,
		Subdomain:     subdomain,
		Domain:        fleetDomain,
		Token:         fleetToken,
		TLSServerName: tlsName,
		Endpoints:     fleetEndpoints,
		TTL:           resolvedTTL,
		Quiet:         *quiet,
	}

	var url string
	if useFRP {
		url = bezhook.PublicURL(subdomain, fleetDomain)
	}
	if !*quiet {
		fmt.Printf("bezhook v%v\n", version)
		if url != "" {
			fmt.Printf("  URL:      %v\n", url)
		} else {
			fmt.Printf("  URL:      (выдаст %s — случайный адрес)\n", strings.Join(providers, "+"))
		}
		fmt.Printf("  local:    %v:%v\n", opts.LocalHost, opts.LocalPort)
		if useFRP {
			fmt.Printf("  gateways: %v\n", joinGateways(fleetEndpoints))
		}
		fmt.Printf("  providers: %v\n", strings.Join(providers, ", "))
		if opts.TTL > 0 {
			fmt.Printf("  ttl:      %v  (--ttl 0 = unlimited)\n", opts.TTL)
		} else {
			fmt.Printf("  ttl:      unlimited\n")
		}
	} else if url != "" {
		fmt.Println(url)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if opts.TTL > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, opts.TTL)
		defer cancel()
		go func() {
			<-ctx.Done()
			if context.Cause(ctx) == context.DeadlineExceeded {
				fmt.Println("bezhook: ttl expired, shutting down")
			}
		}()
	}

	var printedMu sync.Mutex
	printed := map[string]bool{}
	printOnce := func(key, mark, line string) {
		if *quiet {
			return
		}
		full := key + "|" + mark + "|" + line
		printedMu.Lock()
		if printed[full] {
			printedMu.Unlock()
			return
		}
		printed[full] = true
		printedMu.Unlock()
		fmt.Printf("  [%v] %v\n", mark, line)
	}

	var wg sync.WaitGroup
	if useFRP {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := bezhook.RunFanout(ctx, opts, func(st bezhook.GatewayState) {
				mark, line := "!!", st.Phase
				if st.Err != "" {
					line += ": " + st.Err
				}
				if st.Online {
					mark, line = "ok", st.Gateway.String()+" online"
				}
				if !st.Online && st.Phase == "stopped" {
					line = st.Gateway.String() + " stopped"
				}
				printOnce(st.Gateway.String(), mark, line)
			}); err != nil {
				fmt.Fprintln(os.Stderr, "bezhook: frp: "+err.Error())
			}
		}()
	}
	for _, p := range providers {
		switch p {
		case "cloudflared":
			wg.Add(1)
			go func() {
				defer wg.Done()
				if err := bezhook.RunCloudflared(ctx, opts, func(st bezhook.ProviderState) {
					mark, line := "!!", "cloudflared "+st.Err
					if st.Online {
						mark, line = "ok", "cloudflared "+st.URL
					}
					printOnce("cloudflared", mark, line)
				}); err != nil {
					fmt.Fprintln(os.Stderr, "bezhook: cloudflared: "+err.Error())
				}
			}()
		case "ngrok":
			wg.Add(1)
			go func() {
				defer wg.Done()
				if err := bezhook.RunNgrok(ctx, opts, func(st bezhook.ProviderState) {
					mark, line := "!!", "ngrok "+st.Err
					if st.Online {
						mark, line = "ok", "ngrok "+st.URL
					}
					printOnce("ngrok", mark, line)
				}); err != nil {
					fmt.Fprintln(os.Stderr, "bezhook: ngrok: "+err.Error())
				}
			}()
		}
	}
	wg.Wait()
	if !*quiet {
		fmt.Println("bezhook: stopped, subdomain released")
	}
}

func sliceRemove(list []string, item string) []string {
	out := list[:0]
	for _, v := range list {
		if v != item {
			out = append(out, v)
		}
	}
	return out
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, "bezhook: error: "+err.Error())
	os.Exit(1)
}

// reorderLeadingPort moves a leading pure-numeric argument ("bezhook 3000
// --sub x") to the end so the stdlib flag package still sees the flags;
// flag parsing stops at the first positional otherwise.
func reorderLeadingPort(args []string) []string {
	if len(args) == 0 {
		return args
	}
	if n, err := strconv.Atoi(args[0]); err == nil && n > 0 && n <= 65535 {
		out := make([]string, 0, len(args))
		out = append(out, args[1:]...)
		out = append(out, args[0])
		return out
	}
	return args
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

func joinGateways(gws []bezhook.Gateway) string {
	out := ""
	for i, g := range gws {
		if i > 0 {
			out += ", "
		}
		out += g.String()
	}
	return out
}

// osEnvironCompact returns os.Environ as a plain map for Env.
func osEnvironCompact() map[string]string {
	m := map[string]string{}
	for _, kv := range os.Environ() {
		for i := 0; i < len(kv); i++ {
			if kv[i] == '=' {
				m[kv[:i]] = kv[i+1:]
				break
			}
		}
	}
	return m
}
