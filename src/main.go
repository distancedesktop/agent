package main

import (
	"context"
	_ "embed"
	"crypto/tls"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"

	"github.com/quic-go/quic-go"
	"github.com/quic-go/quic-go/http3"
	"github.com/okdaichi/webtransport-go"
	"github.com/qumo-dev/gomoqt/moqt"
	"github.com/qumo-dev/gomoqt/transport"
)

//go:embed web/index.html
var webHTML string

func main() {
	addr := flag.String("addr", ":52020", "WebTransport listen address (UDP)")
	webAddr := flag.String("web", ":52022", "Web UI listen address (TCP)")
	fingerprintOnly := flag.Bool("fingerprint", false, "print the SHA-256 fingerprint and exit")
	customCert := flag.String("cert", "", "TLS certificate path (ECDSA P-256 PEM)")
	customKey := flag.String("key", "", "TLS private key path (ECDSA P-256 PEM)")
	flag.Parse()

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
		CheckOrigin:         func(r *http.Request) bool { return true },
		ApplicationProtocols: []string{"moq-lite-04"},
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

	moqMux := moqt.NewTrackMux(0)
	var moqBroadcastCtx context.Context
	moqBroadcastCtx, moqBroadcastCancel = context.WithCancel(context.Background())

	wtMux.Handle("/moq", &moqt.WebTransportHandler{
		TrackMux: moqMux,
		Config:   &moqt.Config{},
		UpgradeFunc: func(w http.ResponseWriter, r *http.Request) (transport.WebTransportSession, error) {
			s, err := wtUpgrader.Upgrade(w, r)
			if err != nil {
				return nil, err
			}
			return wrapWTSession(s), nil
		},
		Handler: moqt.HandleFunc(func(sess *moqt.Session) {
			log.Printf("moq session from %s", sess.RemoteAddr())
			<-sess.Context().Done()
			log.Printf("moq session closed: %s", sess.RemoteAddr())
		}),
	})

	moqMux.PublishFunc(moqBroadcastCtx, "/video", func(tw *moqt.TrackWriter) {
		name := tw.TrackName
		log.Printf("moq subscriber for /video name=%s", name)
		stateMu.Lock()
		if state != nil {
			state.moqTrackMu.Lock()
			state.moqTracks[tw] = struct{}{}
			log.Printf("moq subscriber added, now %d", len(state.moqTracks))
			state.moqTrackMu.Unlock()
		}
		stateMu.Unlock()
		defer func() {
			stateMu.Lock()
			if state != nil {
				state.moqTrackMu.Lock()
				delete(state.moqTracks, tw)
				state.moqTrackMu.Unlock()
			}
			stateMu.Unlock()
		}()
		<-tw.Context().Done()
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

// -- Web UI --

func startWebUI(addr string, cm *certManager) {
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Write([]byte(webHTML))
	})
	mux.HandleFunc("/api/info", func(w http.ResponseWriter, r *http.Request) {
		ips := localIPs()
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"fingerprint": cm.FingerprintHex(),
			"ips":         ips,
		})
	})
	log.Printf("Web UI on http://%s", addr)
	if err := http.ListenAndServe(addr, mux); err != nil {
		log.Printf("web ui: %v", err)
	}
}

func localIPs() []string {
	var ips []string
	ifaces, err := net.Interfaces()
	if err != nil {
		return ips
	}
	for _, iface := range ifaces {
		addrs, err := iface.Addrs()
		if err != nil {
			continue
		}
		for _, a := range addrs {
			if ipnet, ok := a.(*net.IPNet); ok {
				if ipnet.IP.IsLoopback() || ipnet.IP.IsLinkLocalUnicast() {
					continue
				}
				if ip4 := ipnet.IP.To4(); ip4 != nil {
					ips = append(ips, ip4.String())
				}
			}
		}
	}
	return ips
}
