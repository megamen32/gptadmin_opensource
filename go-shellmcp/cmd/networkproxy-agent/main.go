// Command networkproxy-agent activates one signed Network Tunnel offer on the edge host.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/megamen32/gptadmin/go-shellmcp/internal/networkproxy"
)

func main() {
	offerFile := flag.String("offer-file", "", "JSON file containing one signed Network Tunnel offer")
	mode := flag.String("mode", "file", "offer source: file, pull, or webhook")
	offersURL := flag.String("offers-url", "", "Hub /proxy-agent/offers URL for pull mode")
	webhookListen := flag.String("webhook-listen", "127.0.0.1:8790", "listen address for signed offer webhook mode")
	hubPublicKeyFile := flag.String("hub-public-key-file", "", "file containing the Hub Ed25519 public key")
	agentID := flag.String("agent-id", "", "registered proxy agent ID for signed offers")
	maxSkew := flag.Duration("max-skew", 2*time.Minute, "maximum signed offer clock skew")
	nonceTTL := flag.Duration("nonce-ttl", 10*time.Minute, "signed offer nonce retention")
	dnsServer := flag.String("dns-server", "1.1.1.1:53", "UDP DNS endpoint for edge target resolution; empty uses system DNS")
	flag.Parse()
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	switch *mode {
	case "file":
		if *offerFile == "" {
			log.Fatal("-offer-file is required in file mode")
		}
		body, err := os.ReadFile(*offerFile)
		if err != nil {
			log.Fatalf("read offer: %v", err)
		}
		var offer networkproxy.Offer
		if err := json.Unmarshal(body, &offer); err != nil {
			log.Fatalf("decode offer: %v", err)
		}
		if err := networkproxy.RunOfferWithDNS(ctx, offer, *dnsServer); err != nil && ctx.Err() == nil {
			log.Fatalf("run network tunnel offer: %v", err)
		}
	case "pull", "webhook":
		if err := runDeliveredOffers(ctx, *mode, *offersURL, *webhookListen, *hubPublicKeyFile, *agentID, *maxSkew, *nonceTTL, *dnsServer); err != nil && ctx.Err() == nil {
			log.Fatalf("run network tunnel offers: %v", err)
		}
	default:
		log.Fatalf("-mode must be file, pull, or webhook")
	}
}

func runDeliveredOffers(ctx context.Context, mode, offersURL, webhookListen, publicKeyFile, agentID string, maxSkew, nonceTTL time.Duration, dnsServer string) error {
	if offersURL == "" && mode == "pull" || publicKeyFile == "" || agentID == "" || maxSkew <= 0 || nonceTTL <= 0 {
		return networkproxy.ErrOfferInvalid
	}
	publicKey, err := os.ReadFile(publicKeyFile)
	if err != nil {
		return err
	}
	verifier, err := networkproxy.NewSignedOfferVerifier(networkproxy.OfferVerifierConfig{
		HubPublicKey: strings.TrimSpace(string(publicKey)), AgentID: agentID, MaxSkew: maxSkew, NonceTTL: nonceTTL,
	})
	if err != nil {
		return err
	}
	var source networkproxy.OfferSource
	var server *http.Server
	if mode == "pull" {
		source, err = networkproxy.NewPullOfferSource(&http.Client{}, offersURL, verifier, 1<<20)
		if err != nil {
			return err
		}
	} else {
		handler, delivered, handlerErr := networkproxy.WebhookOfferHandler(verifier, 16, 1<<20)
		if handlerErr != nil {
			return handlerErr
		}
		source = offerChannelSource{offers: delivered}
		server = &http.Server{Addr: webhookListen, Handler: handler}
		go func() {
			if serveErr := server.ListenAndServe(); serveErr != nil && serveErr != http.ErrServerClosed {
				log.Printf("network tunnel webhook stopped: %v", serveErr)
			}
		}()
		defer server.Shutdown(context.Background())
	}
	return (networkproxy.OfferConsumer{}).Run(ctx, source, func(offerCtx context.Context, offer networkproxy.Offer) error {
		return networkproxy.RunOfferWithDNS(offerCtx, offer, dnsServer)
	})
}

type offerChannelSource struct {
	offers <-chan networkproxy.Offer
}

func (source offerChannelSource) Next(ctx context.Context) (networkproxy.Offer, error) {
	select {
	case offer := <-source.offers:
		return offer, nil
	case <-ctx.Done():
		return networkproxy.Offer{}, ctx.Err()
	}
}
