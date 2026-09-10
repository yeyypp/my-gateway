package handlers

import (
	"context"
	"fmt"
	"net/http"
	"sync"
	"time"

	"shuai/gateway/internal/config"
	"shuai/gateway/internal/log"

	"go.uber.org/zap"
)

type ReadyCheck struct {
	upstreams []config.Upstream
	client    *http.Client
}

func NewReadyCheck(upstreams []config.Upstream) *ReadyCheck {
	// An upstream takes part in the readiness decision when it can be probed or
	// when it carries credentials that may be missing. Upstreams with neither a
	// health URL nor an API key environment stay ignored, as before.
	checkedUpstreams := make([]config.Upstream, 0, len(upstreams))
	for _, upstream := range upstreams {
		if upstream.HealthURL != "" || upstream.APIKeyEnv != "" {
			checkedUpstreams = append(checkedUpstreams, upstream)
		}
	}

	client := &http.Client{
		Timeout: 10 * time.Second,
		Transport: &http.Transport{
			MaxIdleConnsPerHost: 3,
			IdleConnTimeout:     60 * time.Second,
		},
	}
	return &ReadyCheck{
		upstreams: checkedUpstreams,
		client:    client,
	}
}

func (rc *ReadyCheck) CheckUpstreams(w http.ResponseWriter, r *http.Request) {
	log.Info("get ready check req")
	numOfUpstreams := len(rc.upstreams)
	if numOfUpstreams == 0 {
		w.WriteHeader(http.StatusOK)
		return
	}

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
	if upstream.APIKeyEnv != "" && upstream.APIKey == "" {
		return fmt.Errorf("upstream %q: API key env %q is not set", upstream.Name, upstream.APIKeyEnv)
	}
	if upstream.HealthURL == "" {
		return nil
	}

	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, upstream.HealthURL, nil)
	if err != nil {
		return err
	}

	resp, err := rc.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("upstream %q returned health status %d", upstream.Name, resp.StatusCode)
	}
	return nil
}
