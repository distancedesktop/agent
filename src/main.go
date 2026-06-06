package main

import (
	"context"
	_ "embed"
	"crypto/tls"
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
	moqMux.PublishFunc(moqBroadcastCtx, "/video", func(tw *moqt.TrackWriter) {
		name := tw.TrackName
		log.Printf("moq subscribe broadcast=/video track=%s", name)

		if name == "catalog.json" {
			stateMu.Lock()
			w, h := 0, 0
			if state != nil {
				w, h = state.width, state.height
			}
			stateMu.Unlock()
			catalog := fmt.Sprintf(`{"version":1,"video":{"renditions":{"video":{"codec":"h264","bitrate":0,"width":%d,"height":%d,"name":"video"}}}}`, w, h)
			g, err := tw.OpenGroup()
			if err != nil {
				log.Printf("moq catalog: open group: %v", err)
				return
			}
			f := moqt.NewFrame(len(catalog))
			f.Write([]byte(catalog))
			if err := g.WriteFrame(f); err != nil {
				log.Printf("moq catalog: write frame: %v", err)
				g.CancelWrite(0)
				return
			}
			g.Close()
			log.Printf("moq catalog served")
			return
		}

		stateMu.Lock()
		if state != nil {
			state.moqTrackMu.Lock()
			state.moqTracks[tw] = struct{}{}
			log.Printf("moq video subscriber added, now %d", len(state.moqTracks))
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

