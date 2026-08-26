package main

import (
	"log"
	"net/http"
	"shuai/gateway/internal/config"
	"shuai/gateway/internal/middleware"
	"shuai/gateway/internal/proxy"
	"shuai/gateway/internal/router"
	"time"

	"github.com/spf13/cobra"
)

func main() {
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
		log.Fatal(err)
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
	log.Println("Starting gateway server on :8080")
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
	for _, upstream := range config.Upstreams {
		proxy, err := proxy.BuildReverseProxy(
			proxy.RoutePolicy{
				UpstreamURL: upstream.BaseURL,
			},
			sharedTransport,
		)
		if err != nil {
			return nil, err
		}

		for _, model := range config.Models {
			if model.Upstream == upstream.Name {
				routes[model.Name] = proxy
			}
		}
	}
	return router.NewModelRouter(routes), nil
}
