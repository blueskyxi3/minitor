package api

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/blueskyxi3/minitor/internal/config"
	"github.com/blueskyxi3/minitor/internal/discovery"
	"github.com/blueskyxi3/minitor/internal/vm"
)

type testEngine struct{ url string }

func (e *testEngine) Address() string                { return e.url }
func (e *testEngine) Apply(context.Context) error    { return nil }
func (e *testEngine) WriteTargets([]vm.Target) error { return nil }

func sign(secret string, claims Claims) string {
	body, _ := json.Marshal(claims)
	part := base64.RawURLEncoding.EncodeToString(body)
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(part))
	return part + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

func TestIdentityIsBoundToPluginMethodURIAndExpiry(t *testing.T) {
	secret := strings.Repeat("s", 32)
	now := time.Now()
	req := httptest.NewRequest("GET", "/api/v1/status?x=1", nil)
	claims := Claims{UserID: 1, PluginID: "monitor", Method: "GET", URI: req.URL.RequestURI(), ExpiresAt: now.Add(30 * time.Second).Unix()}
	if _, ok := Verify(secret, sign(secret, claims), req, now); !ok {
		t.Fatal("valid identity rejected")
	}
	for _, change := range []func(*Claims){func(c *Claims) { c.PluginID = "other" }, func(c *Claims) { c.URI = "/api/v1/status?x=2" }, func(c *Claims) { c.Method = "PUT" }, func(c *Claims) { c.ExpiresAt = now.Add(61 * time.Second).Unix() }, func(c *Claims) { c.ExpiresAt = now.Add(-time.Second).Unix() }} {
		copy := claims
		change(&copy)
		if _, ok := Verify(secret, sign(secret, copy), req, now); ok {
			t.Fatalf("accepted modified identity: %+v", copy)
		}
	}
}

func TestPluginRoutesRecheckHostPermissionsAndRedactSettings(t *testing.T) {
	role := 100
	access := "manage"
	calls := 0
	hostServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.URL.Path != "/api/user/self" || r.Header.Get("Authorization") != "Bearer delegation" || r.Header.Get("X-AIHub-Plugin-ID") != "monitor" {
			t.Error("wrong host permission callback")
		}
		writeJSON(w, 200, map[string]any{"success": true, "data": User{ID: 9, Role: role, PageAccess: map[string]string{"monitor": access}}})
	}))
	defer hostServer.Close()
	host, err := NewHost(hostServer.URL, "")
	if err != nil {
		t.Fatal(err)
	}
	store, err := config.Open(filepath.Join(t.TempDir(), "config.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	settings := store.Get()
	settings.Discovery.RedisURL = "redis://user:redis-secret@localhost/0"
	settings.Metrics.Password = "metric-secret"
	if err := store.Save(settings); err != nil {
		t.Fatal(err)
	}
	engine := &testEngine{}
	server := &Server{Secret: strings.Repeat("s", 32), Host: host, Settings: store, Engine: engine, Discovery: discovery.New(store, engine), Assets: fstest.MapFS{"assets/plugin.js": &fstest.MapFile{Data: []byte("export const plugin=true;")}}, Manifest: []byte(`{"id":"monitor"}`)}
	handler := server.Handler()
	call := func(method, path, body string, identity bool) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		if identity {
			req.Header.Set(IdentityHeader, sign(server.Secret, Claims{UserID: 9, PluginID: "monitor", Method: method, URI: path, ExpiresAt: time.Now().Add(30 * time.Second).Unix()}))
			req.Header.Set(DelegationHeader, "delegation")
		}
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, req)
		return w
	}
	if w := call("GET", "/api/v1/settings", "", false); w.Code != 401 {
		t.Fatal("unsigned request accepted")
	}
	w := call("GET", "/api/v1/settings", "", true)
	if w.Code != 200 || strings.Contains(w.Body.String(), "redis-secret") || strings.Contains(w.Body.String(), "metric-secret") {
		t.Fatalf("invalid settings response: %d %s", w.Code, w.Body.String())
	}
	access = ""
	if w := call("GET", "/api/v1/settings", "", true); w.Code != 403 {
		t.Fatal("revoked permission remained usable")
	}
	access = "read"
	role = 10
	if w := call("PUT", "/api/v1/settings", `{}`, true); w.Code != 403 {
		t.Fatal("non-root updated settings")
	}
	role = 100
	if w := call("PUT", "/api/v1/settings", `{"storage":{"data_path":"/tmp/other"}}`, true); w.Code != 400 {
		t.Fatal("data path writable from API")
	}
	if w := call("GET", "/internal/manifest", "", false); w.Code != 401 {
		t.Fatal("internal manifest was public")
	}
	if w := call("GET", "/assets/plugin.js", "", false); w.Code != 200 {
		t.Fatal("plugin asset did not load")
	}
	if calls < 4 {
		t.Fatal("permissions were not rechecked")
	}
	if w := call("GET", "/api/v1/unknown", "", true); w.Code != 404 {
		t.Fatal("unknown API returned an HTML page")
	}
	before := store.Get().Metrics.Password
	if w := call("PUT", "/api/v1/settings", `{"metrics":{"username":"new"}}`, true); w.Code != 200 {
		t.Fatalf("configuration update: %s", w.Body.String())
	}
	if store.Get().Metrics.Password != before {
		t.Fatal("omitted password was erased")
	}
}

func TestNoRedirectForDelegatedHostCredentials(t *testing.T) {
	leaked := false
	destination := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { leaked = true; w.WriteHeader(200) }))
	defer destination.Close()
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, destination.URL, http.StatusFound) }))
	defer source.Close()
	host, _ := NewHost(source.URL, "")
	_, err := host.CurrentUser(context.Background(), "private-delegation")
	if err == nil || leaked {
		t.Fatal("delegation followed a redirect")
	}
}
