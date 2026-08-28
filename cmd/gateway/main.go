package main

import (
	"fmt"
	"net/http"
	"os"
	"time"

	"shuai/gateway/internal/config"
	"shuai/gateway/internal/log"
	"shuai/gateway/internal/middleware"
	"shuai/gateway/internal/proxy"
	"shuai/gateway/internal/router"

	"github.com/spf13/cobra"
)

func main() {
	if err := log.Init("info", "stdout"); err != nil {
		fmt.Fprintf(os.Stderr, "Init log failed. err:%v", err)
		return
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
		log.Panicf("gateway start failed. err:%v", err)
	}
}

func runGateway(config config.Config) error {
	router, err := buildRouter(config)
	if err != nil {
		return err
	}
	server := &http.Server{
		Addr:         ":8080",
		ReadTimeout:  10 * time.Second,
		WriteTimeout: 10 * time.Second,
		IdleTimeout:  30 * time.Second,
	}
	mux := http.NewServeMux()
	mux.Handle("POST /v1/chat/completions", middleware.ValidateJSONRequest(middleware.RequestTimeout(5*time.Second)(router)))
	server.Handler = mux
	log.Infof("Starting gateway server on :8080")
	return server.ListenAndServe()
}

func buildRouter(config config.Config) (*router.ModelRouter, error) {
	sharedTransport := &http.Transport{
		MaxIdleConns:          5,
		MaxIdleConnsPerHost:   2,
		IdleConnTimeout:       30 * time.Second,
		ResponseHeaderTimeout: 5 * time.Second,
	}

	routes := make(map[string]http.Handler)
loop:
	for _, model := range config.Models {
		for _, upstream := range config.Upstreams {
			if model.Upstream == upstream.Name {
				proxy, err := proxy.BuildReverseProxy(proxy.RoutePolicy{
					UpstreamURL:   upstream.BaseURL,
					UpstreamModel: model.RealModel,
				}, sharedTransport)
				if err != nil {
					log.Errorf("BuildReverseProxy failed. err:%v", err)
					return nil, err
				}
				routes[model.LogicModel] = proxy
				continue loop
			}
		}
	}
	return router.NewModelRouter(routes), nil
}
