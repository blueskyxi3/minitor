package api

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/blueskyxi3/minitor/internal/config"
)

const IdentityHeader = "X-AIHub-Identity"
const DelegationHeader = "X-AIHub-Host-Authorization"

type Claims struct {
	UserID    int    `json:"user_id"`
	PluginID  string `json:"plugin_id"`
	Method    string `json:"method"`
	URI       string `json:"uri"`
	ExpiresAt int64  `json:"expires_at"`
}

func Verify(secret, token string, request *http.Request, now time.Time) (Claims, bool) {
	var claims Claims
	parts := strings.Split(token, ".")
	if len(secret) < 32 || len(parts) != 2 {
		return claims, false
	}
	signature, err := base64.RawURLEncoding.DecodeString(parts[1])
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(parts[0]))
	if err != nil || !hmac.Equal(signature, mac.Sum(nil)) {
		return claims, false
	}
	body, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil || json.Unmarshal(body, &claims) != nil {
		return claims, false
	}
	return claims, claims.UserID > 0 && claims.PluginID == "monitor" && claims.Method == request.Method && claims.URI == request.URL.RequestURI() && claims.ExpiresAt > now.Unix() && claims.ExpiresAt <= now.Add(time.Minute).Unix()
}

type User struct {
	ID         int               `json:"id"`
	Role       int               `json:"role"`
	PageAccess map[string]string `json:"page_access"`
}

func (u User) CanView() bool {
	access := u.PageAccess["monitor"]
	return access == "read" || access == "manage"
}
func (u User) CanConfigure() bool { return u.CanView() && u.Role == 100 }

type Host struct {
	mu         sync.RWMutex
	base       string
	ConfigPath string
	HTTP       *http.Client
}

func NewHost(base, configPath string) (*Host, error) {
	if err := validateOrigin(base); err != nil {
		return nil, err
	}
	return &Host{base: strings.TrimRight(base, "/"), ConfigPath: configPath, HTTP: &http.Client{Timeout: 5 * time.Second, Transport: &http.Transport{MaxIdleConns: 16, MaxIdleConnsPerHost: 8, IdleConnTimeout: 90 * time.Second}, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}, nil
}

func validateOrigin(raw string) error {
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") {
		return errors.New("host URL must be an HTTP/HTTPS origin")
	}
	return nil
}

func (h *Host) Base() string { h.mu.RLock(); defer h.mu.RUnlock(); return h.base }

func (h *Host) SetBase(raw string) error {
	if err := validateOrigin(raw); err != nil {
		return err
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	raw = strings.TrimRight(raw, "/")
	if raw == h.base {
		return nil
	}
	if h.ConfigPath != "" {
		body, err := os.ReadFile(h.ConfigPath)
		if err != nil {
			return errors.New("host runtime configuration is not readable")
		}
		var document map[string]any
		if yaml.Unmarshal(body, &document) != nil {
			return errors.New("invalid host runtime configuration")
		}
		host, _ := document["host"].(map[string]any)
		if host == nil {
			host = map[string]any{}
		}
		host["base_url"] = raw
		document["host"] = host
		encoded, err := yaml.Marshal(document)
		if err != nil {
			return err
		}
		if err := config.WritePrivate(h.ConfigPath, encoded); err != nil {
			return errors.New("host runtime configuration is not writable")
		}
	}
	h.base = raw
	return nil
}

func (h *Host) CurrentUser(ctx context.Context, delegation string) (User, error) {
	var result User
	if delegation == "" {
		return result, errors.New("missing host user delegation")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, h.Base()+"/api/user/self", nil)
	if err != nil {
		return result, err
	}
	req.Header.Set("Authorization", "Bearer "+strings.TrimPrefix(delegation, "Bearer "))
	req.Header.Set("X-AIHub-Plugin-ID", "monitor")
	response, err := h.HTTP.Do(req)
	if err != nil {
		return result, errors.New("cannot verify current host permissions")
	}
	defer response.Body.Close()
	if response.StatusCode != 200 {
		return result, errors.New("host user delegation is unavailable or expired")
	}
	var envelope struct {
		Success bool `json:"success"`
		Data    User `json:"data"`
	}
	if json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(&envelope) != nil || !envelope.Success {
		return result, errors.New("host permission lookup failed")
	}
	return envelope.Data, nil
}
