package main

import (
	"context"
	"crypto/tls"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"time"

	"github.com/okdaichi/webtransport-go"
	"github.com/quic-go/quic-go"
	"github.com/quic-go/quic-go/http3"

	"distancedesktop/agent/src/backend"
)

// backendOpts holds parsed --<backend> flag values.
type backendOpts struct {
	captured string
	sunshine string
	vnc      string
	rdp      string
}

// parseKV parses "k=v,k2=v2" style option strings.
func parseKV(s string) map[string]string {
	out := map[string]string{}
	if s == "" {
		return out
	}
	for _, part := range strings.Split(s, ",") {
		kv := strings.SplitN(part, "=", 2)
		if len(kv) == 2 {
			out[strings.TrimSpace(kv[0])] = strings.TrimSpace(kv[1])
		}
	}
	return out
}

// configureBackends builds the concrete backends from flags.
func configureBackends(o backendOpts) {
	for _, entry := range []struct {
		name string
		opts string
	}{
		{"sunshine", o.sunshine},
		{"vnc", o.vnc},
		{"rdp", o.rdp},
	} {
		if entry.opts == "" {
			continue
		}
		b, err := backend.Get(entry.name)
		if err != nil {
			log.Fatalf("%v", err)
		}
		addr := parseKV(entry.opts)["addr"]
		switch t := b.(type) {
		case *backend.SunshineBackend:
			t.Addr = addr
		case *backend.VNCBackend:
			t.Addr = addr
		case *backend.RDPBackend:
			t.Addr = addr
		}
	}
}

// selectBackend resolves the requested backend, probing candidates in auto order.
func selectBackend(name string) backend.Backend {
	if name == "" || name == "auto" {
		for _, cand := range backend.Candidates() {
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			_, err := cand.Backend.ListDisplays(ctx)
			cancel()
			if err == nil {
				log.Printf("auto: selected backend %q", cand.Name)
				return cand.Backend
			}
			log.Printf("auto: %s unavailable: %v", cand.Name, err)
		}
		log.Fatalf("auto: no usable backend found")
	}
	b, err := backend.Get(name)
	if err != nil {
		log.Fatalf("%v", err)
	}
	return b
}

func main() {
	addr := flag.String("addr", ":52020", "WebTransport listen address (UDP)")
	webAddr := flag.String("web", ":52022", "Web UI listen address (TCP)")
	fingerprintOnly := flag.Bool("fingerprint", false, "print the SHA-256 fingerprint and exit")
	customCert := flag.String("cert", "", "TLS certificate path (ECDSA P-256 PEM)")
	customKey := flag.String("key", "", "TLS private key path (ECDSA P-256 PEM)")

	backendName := flag.String("backend", "auto", fmt.Sprintf("video backend: auto|%s", strings.Join(backend.Names(), "|")))
	var bopts backendOpts
	flag.StringVar(&bopts.captured, "captured", "", `captured backend opts: "source=kms,device=/dev/dri/card1"`)
	flag.StringVar(&bopts.sunshine, "sunshine", "", `sunshine backend opts: "addr=127.0.0.1:47989"`)
	flag.StringVar(&bopts.vnc, "vnc", "", `vnc backend opts: "addr=10.10.1.6:5901"`)
	flag.StringVar(&bopts.rdp, "rdp", "", `rdp backend opts: "addr=10.10.1.6:3389"`)
	dryRun := flag.Bool("dry-run", false, "list displays via the selected backend and exit")
	flag.Parse()

	configureBackends(bopts)
	sel := selectBackend(*backendName)

	if *dryRun {
		displays, err := sel.ListDisplays(context.Background())
		if err != nil {
			log.Fatalf("dry-run: %v", err)
		}
		for _, d := range displays {
			fmt.Printf("display id=%d %dx%d @ (%d,%d) %.2fhz\n", d.ID, d.Width, d.Height, d.X, d.Y, d.RefreshRate)
		}
		return
	}

	// Remember the selection for the session handlers.
	activeBackend = sel

	var tlsConfig *tls.Config
	var cm *certManager

	if *customCert != "" && *customKey != "" {
		cer, err := tls.LoadX509KeyPair(*customCert, *customKey)
		if err != nil {
			log.Fatalf("load cert: %v", err)
		}
		tlsConfig = &tls.Config{
			Certificates: []tls.Certificate{cer},
			NextProtos:   []string{"h3"},
		}
	} else {
		var err error
		cm, err = newCertManager(configDir())
		if err != nil {
			log.Fatalf("cert manager: %v", err)
		}
		if *fingerprintOnly {
			fmt.Println(cm.FingerprintHex())
			return
		}
		tlsConfig = cm.TLSConfig()
	}

	wtUpgrader := &webtransport.Upgrader{
		CheckOrigin: func(r *http.Request) bool {
			origin := r.Header.Get("Origin")
			if origin == "" {
				return true
			}
			host := r.Host
			if origin == "https://"+host || origin == "http://"+host {
				return true
			}
			if cm != nil && *webAddr != "" {
				_, webPort, _ := net.SplitHostPort(*webAddr)
				hostname, _, _ := net.SplitHostPort(host)
				if hostname == "" {
					hostname = host
				}
				allowedWebOrigin := "https://" + net.JoinHostPort(hostname, webPort)
				if origin == allowedWebOrigin {
					return true
				}
			}
			log.Printf("WT upgrade rejected origin %q from %s", origin, r.RemoteAddr)
			return false
		},
		ApplicationProtocols: []string{"moq-lite-04"}, // kept for legacy client compat
	}

	wtServer := &webtransport.Server{
		H3: &http3.Server{
			Addr:      *addr,
			TLSConfig: tlsConfig,
			QUICConfig: &quic.Config{
				EnableDatagrams: true,
			},
		},
	}

	wtMux := http.NewServeMux()

	wtMux.HandleFunc("/wt", func(w http.ResponseWriter, r *http.Request) {
		remote := r.RemoteAddr
		log.Printf("WT upgrade request from %s", remote)
		s, err := wtUpgrader.Upgrade(w, r)
		if err != nil {
			log.Printf("WT upgrade failed from %s: %v", remote, err)
			w.WriteHeader(400)
			return
		}
		log.Printf("WT upgrade succeeded from %s", remote)
		go handleSession(s, cm)
	})

	wtServer.H3.Handler = wtMux

	log.Printf("local IPs: %v", localIPs())

	udpAddr, err := net.ResolveUDPAddr("udp", *addr)
	if err != nil {
		log.Fatalf("resolve udp: %v", err)
	}
	udpConn, err := net.ListenUDP("udp", udpAddr)
	if err != nil {
		log.Fatalf("listen udp %s: %v", *addr, err)
	}

	go func() {
		log.Printf("WebTransport on udp://%s", *addr)
		if err := wtServer.Serve(udpConn); err != nil {
			log.Fatalf("webtransport: %v", err)
		}
	}()

	if cm != nil {
		go certRotationLoop(cm)
		go startWebUI(*webAddr, cm)
	}

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt)
	<-sig

	stateMu.Lock()
	if state != nil {
		teardown()
	}
	stateMu.Unlock()
	wtServer.Close()
}
