package main

import (
	_ "embed"
	"encoding/json"
	"log"
	"net"
	"net/http"
)

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
