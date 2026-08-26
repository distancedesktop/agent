package main

import (
	"crypto/tls"
	"embed"
	"encoding/json"
	"io/fs"
	"log"
	"net"
	"net/http"
	"strings"
)

//go:embed web/dist
var webDistFS embed.FS

// startWebUI serves the built distance-web single-page app (web/dist) over
// HTTPS on :52022 and exposes /api/info for the viewer to auto-discover the
// agent fingerprint + IPs.
//
// HTTPS (not plaintext HTTP) is required so the page is a secure context and
// can open a WebTransport connection with a pinned self-signed certificate.
func startWebUI(addr string, cm *certManager) {
	sub, err := fs.Sub(webDistFS, "web/dist")
	if err != nil {
		log.Fatalf("web ui: embed sub: %v", err)
	}
	fileServer := http.FileServer(http.FS(sub))

	mux := http.NewServeMux()
	mux.HandleFunc("/api/info", func(w http.ResponseWriter, r *http.Request) {
		ips := localIPs()
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"fingerprint": cm.FingerprintHex(),
			"ips":         ips,
		})
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		// SPA fallback: known files are served directly, everything else falls
		// back to index.html so client-side routing works.
		path := strings.TrimPrefix(r.URL.Path, "/")
		if path == "" {
			path = "index.html"
		}
		if _, statErr := fs.Stat(sub, path); statErr != nil {
			data, readErr := fs.ReadFile(sub, "index.html")
			if readErr != nil {
				http.Error(w, "not found", http.StatusNotFound)
				return
			}
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.Write(data)
			return
		}
		fileServer.ServeHTTP(w, r)
	})

	tlsCfg := &tls.Config{
		GetCertificate: cm.getCertificate,
		NextProtos:     []string{"h2", "http/1.1"},
	}
	srv := &http.Server{
		Addr:      addr,
		Handler:   mux,
		TLSConfig: tlsCfg,
	}
	log.Printf("Web UI on https://%s", addr)
	if err := srv.ListenAndServeTLS("", ""); err != nil {
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
