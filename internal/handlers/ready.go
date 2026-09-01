package handlers

import (
	"context"
	"fmt"
	"net/http"
	"shuai/gateway/internal/config"
	"shuai/gateway/internal/log"
	"sync"
	"time"

	"go.uber.org/zap"
)

type ReadyCheck struct {
	upstreams []config.Upstream
	client    *http.Client
}

func NewReadyCheck(upstreams []config.Upstream) *ReadyCheck {
	u := make([]config.Upstream, len(upstreams))
	copy(u, upstreams)

	client := &http.Client{
		Timeout: 10 * time.Second,
		Transport: &http.Transport{
			MaxIdleConnsPerHost: 3,
			IdleConnTimeout:     60 * time.Second,
		},
	}
	return &ReadyCheck{
		upstreams: u,
		client:    client,
	}
}

func (rc *ReadyCheck) CheckUpstreams(w http.ResponseWriter, r *http.Request) {
	numOfUpstreams := len(rc.upstreams)
	var wg sync.WaitGroup
	wg.Add(numOfUpstreams)
	resCh := make(chan error, numOfUpstreams)
	for _, upstream := range rc.upstreams {
		go func(u config.Upstream) {
			defer wg.Done()
			resCh <- rc.checkIfUpstreamReady(r.Context(), u)
		}(upstream)
	}

	go func() {
		wg.Wait()
		close(resCh)
	}()

	var available int
	for err := range resCh {
		if err != nil {
			log.Error("upstream unavailable", zap.Error(err))
		} else {
			available++
		}
	}

	if available >= 1 {
		w.WriteHeader(http.StatusOK)
	} else {
		w.WriteHeader(http.StatusServiceUnavailable)
	}
}

func (rc *ReadyCheck) checkIfUpstreamReady(ctx context.Context, upstream config.Upstream) error {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, "GET", upstream.HealthURL, nil)
	if err != nil {
		return err
	}

	resp, err := rc.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("upstream:%s,url:%s, unavailable", upstream.Name, upstream.BaseURL)
	}
	return nil
}
