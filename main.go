// nx-share is the Album's "Post" button on OpenPak.
//
// On a Switch, Album -> Sharing and Editing -> Post opens the share applet (systemWeb in its
// Share usage) at https://web-<env>.share.srv.nintendo.net/?app_id=...&file_count=...&file_type=...
// and hands the selected captures to the page through window.nx.system.setUploadFile. Nintendo's
// page then posted them to Facebook (Twitter until firmware 16). OpenPak's DNS sends that host
// here, and this page posts the screenshot to OpenVerse instead, as the console's own user.
// docs/protocol.md has the evidence for every step.
package main

import (
	"crypto/tls"
	"log"
	"net/http"
	"os"
	"time"

	"github.com/openpak/nx-common/nxtls"
)

const logPrefix = "[nx-share]"

var debug = os.Getenv("NX_DEBUG") != ""

func main() {
	log.SetFlags(log.LstdFlags | log.Lmicroseconds)
	s := newServerFromEnv()
	addr := getenv("NX_ADDR", ":21000")
	srv := &http.Server{
		Addr:              addr,
		Handler:           s,
		ReadHeaderTimeout: 20 * time.Second,
		// A screenshot upload from a console on a slow link.
		ReadTimeout:  2 * time.Minute,
		WriteTimeout: 2 * time.Minute,
		TLSConfig:    nxtls.ServerConfig(logPrefix, []string{"web-lp1.share.srv.nintendo.net"}, getenv("NX_CA_DIR", "/ca"), debug),
		// HTTP/1.1 only, like the other console listeners: the browser is an old engine.
		TLSNextProto: map[string]func(*http.Server, *tls.Conn, http.Handler){},
	}
	log.Printf("%s listening on %s (TLS); OpenVerse %s, core %s", logPrefix, addr, s.verse.addr, s.coreURL)
	log.Fatal(srv.ListenAndServeTLS("", ""))
}
