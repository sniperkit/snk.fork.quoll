/*
Sniperkit-Bot
- Status: analyzed
*/

package main

import (
	"net/http"
	_ "net/http/pprof"
	"runtime"

	"github.com/v2pro/plz/countlog"

	"github.com/sniperkit/snk.fork.quoll/leaf"
)

func main() {
	runtime.GOMAXPROCS(1)
	logWriter := countlog.NewAsyncLogWriter(
		countlog.LEVEL_DEBUG, countlog.NewFileLogOutput("STDERR"))
	logWriter.EventWhitelist["event!discr.SceneOf"] = true
	logWriter.Start()
	countlog.LogWriters = append(countlog.LogWriters, logWriter)
	err := leaf.RegisterHttpHandlers(http.DefaultServeMux)
	if err != nil {
		countlog.Error("event!agent.start failed", "err", err)
		return
	}
	addr := ":8005"
	countlog.Info("event!agent.start", "addr", addr)
	err = http.ListenAndServe(addr, http.DefaultServeMux)
	countlog.Info("event!agent.stop", "err", err)
}
