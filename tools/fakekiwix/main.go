// Command fakekiwix serves a fake Kiwix catalog and mirrors on localhost.
// It backs the vhs demo tapes in docs/tapes; see internal/fakekiwix.
//
//	go run ./tools/fakekiwix -addr 127.0.0.1:8899 -throttle 6MB
//	curl -X POST 'http://127.0.0.1:8899/_fake/bump?name=wikipedia_en_100'
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"time"

	"github.com/kazie/zimon/internal/config"
	"github.com/kazie/zimon/internal/fakekiwix"
)

func main() {
	if err := run(); err != nil {
		log.Fatal(err)
	}
}

func run() error {
	addr := flag.String("addr", "127.0.0.1:8899", "listen address")
	throttle := flag.String("throttle", "0", "per-connection speed limit, e.g. 6MB (per second)")
	quiet := flag.Bool("quiet", false, "do not print the listening address")
	addrFile := flag.String("addr-file", "", "write the address actually listened on to this file (use with -addr 127.0.0.1:0)")
	flag.Parse()

	limit, err := config.ParseBytes(*throttle)
	if err != nil {
		return err
	}
	fk := fakekiwix.New(fakekiwix.DemoBooks())
	fk.SetThrottle(limit)

	ln, err := (&net.ListenConfig{}).Listen(context.Background(), "tcp", *addr)
	if err != nil {
		return err
	}
	if *addrFile != "" {
		if err := os.WriteFile(*addrFile, []byte(ln.Addr().String()), 0o644); err != nil {
			return err
		}
	}
	if !*quiet {
		fmt.Fprintf(os.Stderr, "fakekiwix: catalog at http://%s/catalog/v2\n", ln.Addr())
	}

	srv := &http.Server{
		Handler:           fk,
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       time.Minute,
		// No WriteTimeout: a throttled download may take minutes.
	}
	return srv.Serve(ln)
}
