package main

import (
	"fmt"
	"net/http"
	"os"
	"time"

	"shuai/gateway/internal/config"
	"shuai/gateway/internal/handlers"
	"shuai/gateway/internal/log"
	"shuai/gateway/internal/middleware"
	"shuai/gateway/internal/protocol"
	"shuai/gateway/internal/proxy"
	"shuai/gateway/internal/router"

	"github.com/spf13/cobra"
	"go.uber.org/zap"
)

func main() {
	if err := log.Init("info", "stdout"); err != nil {
		fmt.Fprintf(os.Stderr, "initialize logger: %v\n", err)
		os.Exit(1)
	}

	rootCmd := &cobra.Command{
		Use:   "gateway start [--config] config_file",
		Short: "gateway for ai model",
	}

	startCmd := &cobra.Command{
		Use: "start [--config] config_file",
		RunE: func(cmd *cobra.Command, args []string) error {
			configFile, err := cmd.Flags().GetString("config")
			if err != nil {
				return err
			}

			config, err := config.Load(configFile)
			if err != nil {
				return err
			}

			return runGateway(config)
		},
	}

	flags := startCmd.Flags()
	flags.String("config", config.DefaultConfig, "config file path")

	rootCmd.AddCommand(startCmd)
	if err := rootCmd.Execute(); err != nil {
		log.Error("gateway stopped", zap.Error(err))
		_ = log.Sync()
		os.Exit(1)
	}
	_ = log.Sync()
}

func runGateway(config config.Config) error {
	router, err := buildRouter(config)
	if err != nil {
		return err
	}
	server := &http.Server{
		Addr:         ":8080",
		ReadTimeout:  30 * time.Second,
		WriteTimeout: 60 * time.Second, // 网关给客户端写响应的超时时间，会在收到请求header时重置
		IdleTimeout:  30 * time.Second,
	}
	server.Handler = newGatewayHandler(config, router)
	log.Info("gateway server starting", zap.String("addr", server.Addr))
	return server.ListenAndServe()
}

func newGatewayHandler(cfg config.Config, modelRouter http.Handler) http.Handler {
	chat := middleware.Chain(
		middleware.Authorization,
		middleware.ConcurrencyLimit,
		middleware.RateLimit,
		middleware.ValidateJSONRequest,
		middleware.RequestTimeout(30*time.Second),
	)(modelRouter)
	api := middleware.Chain(middleware.SetGatewayRequestID, middleware.LogRequest)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" {
			handlers.SendError(w, protocol.ErrorForStatus(http.StatusNotFound), http.StatusNotFound)
			return
		}
		if r.Method != http.MethodPost {
			w.Header().Set("Allow", http.MethodPost)
			handlers.SendError(w, protocol.ErrorForStatus(http.StatusMethodNotAllowed), http.StatusMethodNotAllowed)
			return
		}
		chat.ServeHTTP(w, r)
	}))
	mux := http.NewServeMux()
	mux.Handle("/v1", api)
	mux.Handle("/v1/", api)
	mux.HandleFunc("GET /healthz", HealthCheck)
	readyCheck := handlers.NewReadyCheck(cfg.Upstreams)
	mux.HandleFunc("GET /readyz", readyCheck.CheckUpstreams)
	return mux
}

func buildRouter(config config.Config) (*router.ModelRouter, error) {
	sharedTransport := &http.Transport{
		MaxIdleConns:          5,
		MaxIdleConnsPerHost:   2,
		IdleConnTimeout:       30 * time.Second,
		ResponseHeaderTimeout: 30 * time.Second,
	}

	routes := make(map[string][]*proxy.Proxy)
	upstreams := make(map[string]*proxy.Proxy)
	for _, u := range config.Upstreams {
		rP, err := proxy.BuildReverseProxy(proxy.RoutePolicy{
			UpstreamName: u.Name,
			UpstreamURL:  u.BaseURL,
			DefaultModel: u.DefaultModel,
		}, sharedTransport, u.APIKey)
		if err != nil {
			log.Error("build reverse proxy failed", zap.Error(err))
			return nil, err
		}
		upstreams[u.Name] = rP
	}

	for _, m := range config.Models {
		uList := make([]*proxy.Proxy, 0, len(m.Upstreams))
		for _, uname := range m.Upstreams {
			if u, ok := upstreams[uname]; ok {
				uList = append(uList, u)
			}
		}
		routes[m.LogicModel] = uList
	}

	return router.NewModelRouter(routes), nil
}

func HealthCheck(w http.ResponseWriter, r *http.Request) {
	log.Info("get health check req")
	w.WriteHeader(http.StatusOK)
}
