package main

import (
	"embed"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

//go:embed assets/*
var embedded embed.FS

func readAsset(name string) []byte {
	b, err := embedded.ReadFile("assets/" + name)
	if err != nil {
		panic(err)
	}
	return b
}
func openBrowser(u string) error {
	switch runtime.GOOS {
	case "windows":
		return exec.Command("rundll32", "url.dll,FileProtocolHandler", u).Start()
	case "darwin":
		return exec.Command("open", u).Start()
	default:
		return exec.Command("xdg-open", u).Start()
	}
}
func newLogger(dir string) *log.Logger {
	_ = os.MkdirAll(filepath.Join(dir, "diagnostics"), 0755)
	f, err := os.OpenFile(filepath.Join(dir, "diagnostics", "corridor-engine.log"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0644)
	if err != nil {
		return log.New(os.Stderr, "", log.LstdFlags|log.Lmicroseconds)
	}
	return log.New(f, "", log.LstdFlags|log.Lmicroseconds)
}

func configuredOlexDir(defaultDir string, logger *log.Logger) string {
	if v := strings.TrimSpace(os.Getenv("LINDBLAD_OLEX_LIBRARY")); v != "" {
		if err := os.MkdirAll(v, 0755); err == nil {
			logger.Printf("using OLEX library from LINDBLAD_OLEX_LIBRARY: %s", v)
			return v
		}
	}
	exe, err := os.Executable()
	if err == nil {
		configFile := filepath.Join(filepath.Dir(exe), "OLEX_LIBRARY_PATH.txt")
		if b, readErr := os.ReadFile(configFile); readErr == nil {
			v := strings.TrimSpace(strings.Trim(string(b), `"`))
			if v != "" {
				if !filepath.IsAbs(v) {
					v = filepath.Join(filepath.Dir(exe), v)
				}
				if mkErr := os.MkdirAll(v, 0755); mkErr == nil {
					logger.Printf("using configured OLEX library: %s", v)
					return v
				} else {
					logger.Printf("configured OLEX library unavailable (%s): %v", v, mkErr)
				}
			}
		}
	}
	return defaultDir
}

type launchOptions struct {
	listen    string
	noBrowser bool
	selfTest  bool
	public    bool
}

func configuredPublicListen() string {
	port := strings.TrimSpace(os.Getenv("PORT"))
	if port == "" {
		port = "8787"
	}
	return "0.0.0.0:" + port
}

func parseLaunchOptions(args []string) launchOptions {
	opts := launchOptions{listen: "127.0.0.1:0"}
	for i := 0; i < len(args); i++ {
		a := strings.TrimSpace(args[i])
		switch {
		case a == "--no-browser":
			opts.noBrowser = true
		case a == "--self-test":
			opts.selfTest = true
		case a == "--public":
			opts.public = true
			if opts.listen == "127.0.0.1:0" {
				opts.listen = configuredPublicListen()
			}
			opts.noBrowser = true
		case a == "--lan":
			opts.listen = "0.0.0.0:8787"
		case strings.HasPrefix(a, "--listen="):
			opts.listen = strings.TrimSpace(strings.TrimPrefix(a, "--listen="))
		case a == "--listen" && i+1 < len(args):
			i++
			opts.listen = strings.TrimSpace(args[i])
		}
	}
	if opts.listen == "" {
		opts.listen = "127.0.0.1:0"
	}
	return opts
}

func configuredDataDir(defaultDir string) string {
	if v := strings.TrimSpace(os.Getenv("LINDBLAD_DATA_ROOT")); v != "" {
		if err := os.MkdirAll(v, 0755); err == nil {
			return v
		}
	}
	if exe, err := os.Executable(); err == nil {
		configFile := filepath.Join(filepath.Dir(exe), "DATA_LIBRARY_PATH.txt")
		if b, readErr := os.ReadFile(configFile); readErr == nil {
			v := strings.TrimSpace(strings.Trim(string(b), `"`))
			if v != "" {
				if !filepath.IsAbs(v) {
					v = filepath.Join(filepath.Dir(exe), v)
				}
				if err := os.MkdirAll(v, 0755); err == nil {
					return v
				}
			}
		}
	}
	return defaultDir
}

func main() {
	opts := parseLaunchOptions(os.Args[1:])
	dir := configuredDataDir(localDataDir())
	logger := newLogger(dir)
	logger.Printf("starting AI Corridor engine %s/%s", runtime.GOOS, runtime.GOARCH)
	basePlanner, err := LoadPlannerData(readAsset("planner.bin.gz"))
	if err != nil {
		fatalUI(logger, "Planner database failed to load", err)
		return
	}
	land := LoadLandMask(readAsset("land.geojson"))
	assets := map[string][]byte{
		"index.html": readAsset("index.html"), "styles.css": readAsset("styles.css"),
		"app.js": readAsset("app.js"), "library.js": readAsset("library.js"),
		"public_index.html": readAsset("public_index.html"), "public_app.js": readAsset("public_app.js"), "public_library.js": readAsset("public_library.js"), "cloud_editor.js": readAsset("cloud_editor.js"),
		"login.html": readAsset("login.html"), "login.css": readAsset("login.css"), "login.js": readAsset("login.js"),
		"account.js": readAsset("account.js"), "account.css": readAsset("account.css"),
		"admin.html": readAsset("admin.html"), "admin.css": readAsset("admin.css"), "admin.js": readAsset("admin.js"),
	}
	if opts.public {
		publicServer, e := NewPublicServer(dir, basePlanner, land, readAsset("default_olex.olxidx.gz"), assets, logger)
		if e != nil {
			fatalUI(logger, "Public Route Planner could not start", e)
			return
		}
		u, e := publicServer.Serve(opts.listen)
		if e != nil {
			fatalUI(logger, "Public Route Planner could not start", e)
			return
		}
		logger.Printf("public multi-user server listening on %s", u)
		<-publicServer.done
		return
	}

	olexDir := configuredOlexDir(filepath.Join(dir, "ai_corridor_olex"), logger)
	library, err := NewOlexLibrary(olexDir, readAsset("default_olex.olxidx.gz"))
	if err != nil {
		fatalUI(logger, "OLEX library failed to load", err)
		return
	}
	rtzLibrary, err := NewRTZLibrary(filepath.Join(dir, "rtz_library"))
	if err != nil {
		fatalUI(logger, "RTZ library failed to load", err)
		return
	}
	planner, err := rtzLibrary.BuildPlanner(basePlanner)
	if err != nil {
		fatalUI(logger, "Stored RTZ routes failed to load", err)
		return
	}
	if opts.selfTest {
		if err := selfTest(basePlanner, library, land); err != nil {
			logger.Printf("self-test failed: %v", err)
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		fmt.Println("SELF-TEST PASSED")
		return
	}
	app := NewApp(basePlanner, planner, library, rtzLibrary, land, filepath.Join(dir, "persistent_uploads"), assets, logger)
	app.recoverPendingOlexImport()
	u, err := app.Serve(opts.listen)
	if err != nil {
		fatalUI(logger, "Route Planner could not start", err)
		return
	}
	logger.Printf("listening on %s", u)
	if !opts.noBrowser {
		_ = openBrowser(u)
	}
	<-app.done
}

func fatalUI(logger *log.Logger, title string, err error) {
	logger.Printf("%s: %v", title, err)
	fmt.Fprintf(os.Stderr, "%s: %v\n", title, err)
}
func selfTest(p *PlannerData, l *OlexLibrary, land *LandMask) error {
	if len(p.Nodes) != 29082 || len(p.RawEdges) != 336266 {
		return fmt.Errorf("planner counts do not match: %d nodes %d edges", len(p.Nodes), len(p.RawEdges))
	}
	comp, err := l.CompositeForCorridor(-65.13, -64.04, -65.02, -63.87)
	if err != nil {
		return err
	}
	req := RouteRequest{Start: "-65.1191, -64.0165", End: "-65.0316, -63.8869", RouteName: "Lemaire Corridor Self Test", DepartureDate: "2026-07-30", DepartureTime: "12:00", DepartureZone: "UTC", ArrivalDate: "2026-07-30", ArrivalTime: "14:00", ArrivalZone: "UTC", MinimumWaypoints: true, AddComments: true}
	plan, err := p.Generate(req, comp, land)
	if err != nil {
		return err
	}
	if len(plan.Waypoints) < 2 || plan.DistanceNM <= 0 {
		return fmt.Errorf("invalid self-test route")
	}
	if plan.CorridorCenteredPct <= 0 {
		return fmt.Errorf("corridor centering was not activated on Lemaire test")
	}
	if _, err = ExportRTZ(plan); err != nil {
		return err
	}
	if b, e := ExportOlexPlot(plan); e != nil || len(b) < 50 {
		return fmt.Errorf("OLEX export failed: %v", e)
	}
	fmt.Printf("Route %.2f NM, %d WPs, corridor-centred %.1f%%, OLEX supported %.1f%%\n", plan.DistanceNM, plan.WaypointCount, plan.CorridorCenteredPct, plan.SupportedPct)
	return nil
}
