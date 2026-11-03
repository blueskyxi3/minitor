package query

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"time"
)

type Sample struct {
	Timestamp int64
	Value     *float64
}

func (s *Sample) UnmarshalJSON(body []byte) error {
	var wire []json.RawMessage
	if json.Unmarshal(body, &wire) != nil || len(wire) != 2 {
		return errors.New("invalid metrics sample")
	}
	var timestamp float64
	var raw string
	if json.Unmarshal(wire[0], &timestamp) != nil || json.Unmarshal(wire[1], &raw) != nil || math.IsNaN(timestamp) || math.IsInf(timestamp, 0) || timestamp <= 0 || timestamp >= math.MaxInt64 {
		return errors.New("invalid metrics timestamp")
	}
	s.Timestamp = int64(timestamp)
	number, err := strconv.ParseFloat(raw, 64)
	if err != nil {
		return errors.New("invalid metrics value")
	}
	if !math.IsNaN(number) && !math.IsInf(number, 0) {
		s.Value = &number
	}
	return nil
}

type Series struct {
	Metric map[string]string `json:"metric"`
	Value  Sample            `json:"value"`
	Values []Sample          `json:"values"`
}

type Client struct {
	BaseURL string
	HTTP    *http.Client
}

var internalHTTP = &http.Client{Timeout: 8 * time.Second, Transport: &http.Transport{MaxIdleConns: 16, MaxIdleConnsPerHost: 8, IdleConnTimeout: 90 * time.Second}, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}

func NewClient(base string) *Client {
	return &Client{BaseURL: base, HTTP: internalHTTP}
}

func (c *Client) Get(ctx context.Context, path string, params url.Values, out any) error {
	u, err := url.Parse(c.BaseURL + path)
	if err != nil {
		return errors.New("invalid internal metrics address")
	}
	u.RawQuery = params.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return err
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return errors.New("embedded time series database is unavailable")
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return fmt.Errorf("time series query failed (HTTP %d)", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 16<<20+1))
	if err != nil || len(body) > 16<<20 {
		return errors.New("time series response is too large or unreadable")
	}
	var envelope struct {
		Status string          `json:"status"`
		Data   json.RawMessage `json:"data"`
	}
	if json.Unmarshal(body, &envelope) != nil || envelope.Status != "success" {
		return errors.New("invalid time series database response")
	}
	if err := json.Unmarshal(envelope.Data, out); err != nil {
		return errors.New("invalid time series data")
	}
	return nil
}

func (c *Client) Query(ctx context.Context, expression string, filter Filter, instant bool) ([]Series, error) {
	params := url.Values{"query": []string{expression}}
	path, expected := "/api/v1/query_range", "matrix"
	if instant {
		path, expected = "/api/v1/query", "vector"
		params.Set("time", strconv.FormatInt(filter.End, 10))
	} else {
		params.Set("start", strconv.FormatInt(filter.GridStart(), 10))
		params.Set("end", strconv.FormatInt(filter.End, 10))
		params.Set("step", strconv.FormatInt(filter.Step, 10))
	}
	var result struct {
		ResultType string   `json:"resultType"`
		Result     []Series `json:"result"`
	}
	if err := c.Get(ctx, path, params, &result); err != nil {
		return nil, err
	}
	if result.ResultType != expected {
		return nil, errors.New("unexpected metrics result type")
	}
	return result.Result, nil
}
