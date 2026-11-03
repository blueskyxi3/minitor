package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/blueskyxi3/minitor/internal/discovery"
)

func TestHostRegistryClientUsesServiceIdentityAndCurrentHost(t *testing.T) {
	secret := strings.Repeat("s", 32)
	serve := func(id string) *httptest.Server {
		return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method != http.MethodGet || r.URL.RequestURI() != "/api/plugin-host/monitor/instances" ||
				r.Header.Get("X-AIHub-Plugin-ID") != "monitor" || r.Header.Get("X-AIHub-Plugin-Secret") != secret ||
				r.Header.Get("Authorization") != "" || r.Header.Get("Cookie") != "" {
				t.Error("incorrect host registry service request")
			}
			writeJSON(w, 200, map[string]any{"success": true, "data": map[string]any{"instances": []discovery.Instance{{ID: id}}}})
		}))
	}
	first, second := serve("cluster-a"), serve("cluster-b")
	defer first.Close()
	defer second.Close()
	host, err := NewHost(first.URL, "")
	if err != nil {
		t.Fatal(err)
	}
	rows, err := host.MonitorInstances(context.Background(), secret)
	if err != nil || len(rows) != 1 || rows[0].ID != "cluster-a" {
		t.Fatalf("initial host: %+v %v", rows, err)
	}
	if err := host.SetBase(second.URL); err != nil {
		t.Fatal(err)
	}
	rows, err = host.MonitorInstances(context.Background(), secret)
	if err != nil || len(rows) != 1 || rows[0].ID != "cluster-b" {
		t.Fatalf("changed host: %+v %v", rows, err)
	}
}

func TestHostRegistryClientRejectsRedirectsAndMalformedResponses(t *testing.T) {
	called := false
	destination := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { called = true; w.WriteHeader(200) }))
	defer destination.Close()
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, destination.URL, http.StatusFound) }))
	defer source.Close()
	host, _ := NewHost(source.URL, "")
	if _, err := host.MonitorInstances(context.Background(), strings.Repeat("s", 32)); err == nil || called {
		t.Fatal("service credentials followed a redirect")
	}
	for _, body := range []string{`{"success":true,"data":{}}`, `{"success":false,"data":{"instances":[]}}`, `{"success":true,"data":{"instances":null}}`, `{"success":true,"data":{"instances":[]}} trailing`} {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte(body)) }))
		host, _ = NewHost(server.URL, "")
		_, err := host.MonitorInstances(context.Background(), strings.Repeat("s", 32))
		server.Close()
		if err == nil {
			t.Fatalf("accepted malformed host response: %s", body)
		}
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(503)
		_ = json.NewEncoder(w).Encode(map[string]string{"message": "must-not-return-host-credentials"})
	}))
	defer server.Close()
	host, _ = NewHost(server.URL, "")
	if _, err := host.MonitorInstances(context.Background(), strings.Repeat("s", 32)); err == nil || strings.Contains(err.Error(), "must-not-return") {
		t.Fatal("host failure was hidden or leaked its response")
	}
}
