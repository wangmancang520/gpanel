package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"gpanel/deploy"
	"gpanel/internal/auth"
	"gpanel/internal/certs"
	"gpanel/internal/config"
	"gpanel/internal/events"
	"gpanel/internal/files"
	"gpanel/internal/httpx"
	"gpanel/internal/instance"
	"gpanel/internal/runtimes"
	"gpanel/internal/system"
	"gpanel/web"
)

// version is set at build time: -ldflags "-X main.version=v1.2.3".
var version = "dev"

func main() {
	cfg := config.Load()
	switch {
	case cfg.ShowVersion:
		fmt.Println(version)
		return
	case cfg.PrintUnit:
		fmt.Print(deploy.SystemdUnit)
		return
	case cfg.PrintEnv:
		fmt.Print(deploy.EnvTemplate)
		return
	case cfg.ResetPassword:
		pw, err := auth.ResetPassword(cfg.Path("users.json"), cfg.User)
		if err != nil {
			log.Fatalf("重置密码失败: %v", err)
		}
		fmt.Println(pw)
		return
	}
	if err := os.MkdirAll(cfg.DataDir, 0o700); err != nil {
		log.Fatal(err)
	}
	log.Printf("GPanel %s", version)

	bus := events.NewBus()

	tlsMgr, err := certs.New(cfg.Path("tls"), cfg.DisableTLS)
	if err != nil {
		log.Fatal(err)
	}
	issuer, err := certs.NewIssuer(tlsMgr, os.Getenv("GPANEL_ACME_DNS_RESOLVER"))
	if err != nil {
		log.Fatal(err)
	}
	go issuer.RenewLoop(context.Background())

	authSvc, err := auth.New(cfg.Path("users.json"))
	if err != nil {
		log.Fatal(err)
	}

	instances, err := instance.NewManager(cfg.DataDir, bus)
	if err != nil {
		log.Fatal(err)
	}

	rts := runtimes.NewRegistry(&runtimes.Java{
		ManagedDir: cfg.Path("runtimes", "java"),
		Adoptium:   &runtimes.Adoptium{Mirror: cfg.JavaMirror},
	})
	rts.InUse = func(runtimeID string) []string {
		var names []string
		for _, s := range instances.List() {
			if s.Runtime == runtimeID {
				names = append(names, s.Name)
			}
		}
		return names
	}
	rts.Scan(context.Background())
	// Older versions keyed managed runtimes by absolute path; rewrite such
	// references to the current, location-independent IDs.
	for _, s := range instances.List() {
		if rt, ok := rts.Get(s.Runtime); ok && rt.ID != s.Runtime {
			cfg := s.Config
			cfg.Runtime = rt.ID
			if _, err := instances.Update(cfg.ID, cfg); err != nil {
				log.Printf("迁移实例 %s 的运行时失败: %v", cfg.Name, err)
			}
		}
	}
	instances.AddLaunchHook(rts.LaunchHook())

	monitor := system.NewMonitor(cfg.DataDir, version, func() map[string]int {
		pids := map[string]int{}
		for _, s := range instances.List() {
			if s.Pid > 0 {
				pids[s.ID] = s.Pid
			}
		}
		return pids
	})
	go monitor.Run(context.Background())

	// Every feature registers its routes here; add new modules to this list.
	modules := []httpx.Module{
		authSvc,
		instance.NewHandler(instances),
		files.NewHandler("/api/instances", instances.WorkDir),
		runtimes.NewHandler(rts),
		monitor,
		tlsMgr,
		issuer,
	}
	mux := http.NewServeMux()
	for _, m := range modules {
		m.Register(mux)
	}
	mux.Handle("/", web.Handler(cfg.WebDir))

	srv := &http.Server{
		Addr: cfg.Addr,
		// HTTP-01 challenges are answered before the HTTPS redirect and auth.
		Handler:           issuer.ChallengeHandler(tlsMgr.Redirect(authSvc.Middleware(mux))),
		ReadHeaderTimeout: 10 * time.Second,
	}

	instances.StartAutoStart()

	// One port for both protocols: HTTPS can be switched on from the panel.
	ln, err := tlsMgr.Listen(cfg.Addr)
	if err != nil {
		log.Fatal(err)
	}
	go func() {
		scheme := "http"
		if tlsMgr.Active() {
			scheme = "http(s)"
		}
		log.Printf("面板已启动: %s://%s  数据目录: %s", scheme, cfg.Addr, cfg.DataDir)
		if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatal(err)
		}
	}()

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
	<-sig
	log.Println("正在关闭，停止所有实例…")
	instances.Shutdown(60 * time.Second)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = srv.Shutdown(ctx)
}
