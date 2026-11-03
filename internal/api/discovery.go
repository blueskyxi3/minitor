package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"

	"github.com/blueskyxi3/minitor/internal/discovery"
)

// MonitorInstances reads the host's fixed registry projection, never Redis credentials.
func (h *Host) MonitorInstances(ctx context.Context, secret string) ([]discovery.Instance, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, h.Base()+"/api/plugin-host/monitor/instances", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("X-AIHub-Plugin-ID", "monitor")
	req.Header.Set("X-AIHub-Plugin-Secret", secret)
	response, err := h.HTTP.Do(req)
	if err != nil {
		return nil, errors.New("host instance discovery is unavailable")
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("host instance discovery failed (HTTP %d)", response.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, 2<<20+1))
	if err != nil || len(body) > 2<<20 {
		return nil, errors.New("host instance registry response is too large or unreadable")
	}
	var envelope struct {
		Success bool `json:"success"`
		Data    struct {
			Instances []discovery.Instance `json:"instances"`
		} `json:"data"`
	}
	if json.Unmarshal(body, &envelope) != nil || !envelope.Success || envelope.Data.Instances == nil || len(envelope.Data.Instances) > 1000 {
		return nil, errors.New("invalid host instance registry response")
	}
	return envelope.Data.Instances, nil
}
