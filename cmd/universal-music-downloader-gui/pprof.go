//go:build pprof

package main

import (
	"net/http"
	_ "net/http/pprof"
)

// Only in builds made with -tags pprof: profiles of a running window app, on this computer.
func init() {
	go http.ListenAndServe("127.0.0.1:6061", nil)
}
