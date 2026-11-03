package query

import (
	"errors"
	"net/url"
	"strconv"
	"strings"
	"time"
)

type Filter struct {
	Start    int64  `json:"start_timestamp"`
	End      int64  `json:"end_timestamp"`
	Step     int64  `json:"grain_seconds"`
	Scope    string `json:"scope"`
	Model    string `json:"model_name"`
	Channel  string `json:"channel_id"`
	Instance string `json:"instance_id"`
	APIMode  string `json:"api_mode"`
	Stream   string `json:"stream"`
	Stage    string `json:"stage"`
}

func ParseFilter(v url.Values, now time.Time) (Filter, error) {
	f := Filter{Start: now.Add(-time.Hour).Unix(), End: now.Unix(), Step: 60, Scope: "gateway"}
	for key, ptr := range map[string]*int64{"start_timestamp": &f.Start, "end_timestamp": &f.End, "grain_seconds": &f.Step} {
		if raw := v.Get(key); raw != "" {
			n, err := strconv.ParseInt(raw, 10, 64)
			if err != nil {
				return f, errors.New("invalid time range")
			}
			*ptr = n
		}
	}
	if raw := v.Get("scope"); raw != "" {
		f.Scope = raw
	}
	if f.Scope != "gateway" && f.Scope != "model" && f.Scope != "billing" {
		return f, errors.New("invalid monitoring scope")
	}
	if f.Start <= 0 || f.End <= f.Start || f.End > now.Unix()+60 || f.End-f.Start > 30*86400 || f.Step < 15 || f.Step > 86400 || (f.End-f.Start)/f.Step+1 > 1500 {
		return f, errors.New("range must be at most 30 days and contain at most 1500 points")
	}
	f.Model, f.Channel, f.Instance = v.Get("model_name"), v.Get("channel_id"), v.Get("instance_id")
	f.APIMode, f.Stream, f.Stage = v.Get("api_mode"), v.Get("stream"), v.Get("stage")
	if f.Stream != "" && f.Stream != "true" && f.Stream != "false" {
		return f, errors.New("stream must be true or false")
	}
	if f.Channel != "" {
		if n, err := strconv.Atoi(f.Channel); err != nil || n < 1 {
			return f, errors.New("invalid channel ID")
		}
	}
	for _, value := range []string{f.Model, f.Instance, f.APIMode, f.Stage} {
		if len(value) > 256 || strings.ContainsAny(value, "\r\n\x00") {
			return f, errors.New("invalid metrics filter")
		}
	}
	if f.Scope == "billing" && (f.Model != "" || f.Channel != "" || f.APIMode != "" || f.Stream != "") {
		return f, errors.New("billing metrics do not support model/channel/API/stream filters")
	}
	return f, nil
}

const (
	DimInstance = 1 << iota
	DimModel
	DimChannel
	DimAPI
	DimStream
	DimStage
)

// VictoriaMetrics evaluates range queries on a UTC step grid. Use the same
// grid when requesting and filling gaps, while keeping summary bounds intact.
func (f Filter) GridStart() int64 { return (f.Start + f.Step - 1) / f.Step * f.Step }

func (f Filter) Compatible(dimensions int) bool {
	return (f.Instance == "" || dimensions&DimInstance != 0) && (f.Model == "" || dimensions&DimModel != 0) && (f.Channel == "" || dimensions&DimChannel != 0) && (f.APIMode == "" || dimensions&DimAPI != 0) && (f.Stream == "" || dimensions&DimStream != 0) && (f.Stage == "" || dimensions&DimStage != 0)
}

func (f Filter) Selector(metric string, dimensions int, extra ...string) string {
	labels := []string{`job="ai-hub-monitor"`}
	for _, item := range []struct {
		name, value string
		dim         int
	}{{"gateway_instance", f.Instance, DimInstance}, {"model", f.Model, DimModel}, {"channel_id", f.Channel, DimChannel}, {"api_mode", f.APIMode, DimAPI}, {"stream", f.Stream, DimStream}, {"stage", f.Stage, DimStage}} {
		if item.value != "" && dimensions&item.dim != 0 {
			labels = append(labels, item.name+"="+strconv.Quote(item.value))
		}
	}
	labels = append(labels, extra...)
	return metric + "{" + strings.Join(labels, ",") + "}"
}
