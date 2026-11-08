// universal-music-downloader-gui is the app's window (Qt), over the same backend as the
// terminal app (universal-music-downloader). It takes the same --config and --data options.
package main

import (
	"flag"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"syscall"
	"time"

	"universal-music-downloader/internal/core"
	"universal-music-downloader/internal/gui"
)

var version = "dev"

func main() {
	dropOpenGL()
	core.TuneMemory()
	fs := flag.NewFlagSet("universal-music-downloader-gui", flag.ExitOnError)
	configPath := fs.String("config", core.DefaultConfigPath(), "settings file")
	dataDir := fs.String("data", core.DefaultDataDir(), "where the app keeps its state, logs and caches")
	fs.BoolVar(&core.Verbose, "v", false, "detailed log")
	showVersion := fs.Bool("version", false, "print the version and exit")
	fs.Parse(os.Args[1:])
	if *showVersion {
		fmt.Println("Universal Music Downloader (window)", version)
		return
	}
	if *configPath == core.DefaultConfigPath() && *dataDir == core.DefaultDataDir() {
		core.MigrateLegacyFolders()
	}
	*configPath, *dataDir = core.CleanPath(*configPath), core.CleanPath(*dataDir)
	cfg, err := core.LoadConfig(*configPath)
	if err == nil {
		err = cfg.Validate()
	}
	if err == nil {
		err = core.CheckDataDir(cfg, *dataDir)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "Error:", err)
		os.Exit(1)
	}
	if _, err := os.Stat(*configPath); os.IsNotExist(err) {
		core.SaveConfig(*configPath, cfg)
	}
	os.MkdirAll(filepath.Join(*dataDir, "logs"), 0o755)
	if f, err := os.Create(filepath.Join(*dataDir, "logs", time.Now().Format("20060102-150405")+"-gui.log")); err == nil {
		defer f.Close()
		core.DebugLog = log.New(f, "", log.LstdFlags)
		debug.SetCrashOutput(f, debug.CrashOptions{}) // should it ever crash, the log says where
	}
	m := core.NewManager(*configPath, *dataDir, cfg)
	if err := m.Start(); err != nil {
		fmt.Fprintln(os.Stderr, "Error:", err)
		os.Exit(1)
	}
	gui.Version = version
	gui.StateFile = filepath.Join(*dataDir, "window.json")
	gui.Run(m)
	m.Stop()
}

// dropOpenGL starts the program over without the variable that picks a graphics driver: the
// window draws in software, but with __GLX_VENDOR_LIBRARY_NAME set, linking Qt loads the whole
// driver (some 90 MB with NVIDIA's) just in case.
func dropOpenGL() {
	if runtime.GOOS != "linux" || os.Getenv("__GLX_VENDOR_LIBRARY_NAME") == "" {
		return
	}
	os.Unsetenv("__GLX_VENDOR_LIBRARY_NAME")
	if exe, err := os.Executable(); err == nil {
		syscall.Exec(exe, os.Args, os.Environ()) // on failure, carry on as we are
	}
}
