package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"tofi-core/internal/daemon"
	"tofi-core/internal/server"

	"gopkg.in/yaml.v3"
)

// apiClient is a lightweight HTTP client for talking to the Tofi daemon.
type apiClient struct {
	baseURL string
	token   string
	http    *http.Client
}

func newAPIClient() *apiClient {
	c := &apiClient{
		baseURL: fmt.Sprintf("http://localhost:%d", startPort),
		http:    &http.Client{Timeout: 10 * time.Second},
	}
	c.token = c.loadToken()
	return c
}

// cliConfig represents the fields we read from config.yaml for auth.
type cliConfig struct {
	AuthMode    string `yaml:"auth_mode"`
	AccessToken string `yaml:"access_token"`
	JWTSecret   string `yaml:"jwt_secret"`
}

// loadToken reads config.yaml and returns a token for API auth.
// Token mode: use access_token directly.
// Password mode: generate JWT from jwt_secret.
func (c *apiClient) loadToken() string {
	configPath := filepath.Join(homeDir, "config.yaml")
	data, err := os.ReadFile(configPath)
	if err != nil {
		return ""
	}

	var cfg cliConfig
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return ""
	}

	// Token mode: use the stored access token directly
	if cfg.AuthMode == "token" && cfg.AccessToken != "" {
		return cfg.AccessToken
	}

	// Password mode: generate a JWT using the shared secret
	if cfg.JWTSecret != "" {
		os.Setenv("TOFI_JWT_SECRET", cfg.JWTSecret)
		server.InitAuth()
		token, err := server.GenerateToken("admin", "admin")
		if err != nil {
			return ""
		}
		return token
	}

	return ""
}

// ensureRunning checks if the daemon is reachable.
func (c *apiClient) ensureRunning() error {
	if !daemon.CheckHealth(startPort) {
		return fmt.Errorf("engine is not running — start it with: tofi start")
	}
	return nil
}

// doRequest performs an HTTP request with auth header.
func (c *apiClient) doRequest(method, path string, body io.Reader) (*http.Response, error) {
	req, err := http.NewRequest(method, c.baseURL+path, body)
	if err != nil {
		return nil, err
	}
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	return c.http.Do(req)
}

// get performs a GET request and decodes JSON into dest.
func (c *apiClient) get(path string, dest any) error {
	resp, err := c.doRequest("GET", path, nil)
	if err != nil {
		return fmt.Errorf("cannot connect to engine: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("API error (%d): %s", resp.StatusCode, string(body))
	}

	return json.NewDecoder(resp.Body).Decode(dest)
}

// getRaw performs a GET and returns the raw body bytes.
func (c *apiClient) getRaw(path string) ([]byte, error) {
	resp, err := c.doRequest("GET", path, nil)
	if err != nil {
		return nil, fmt.Errorf("cannot connect to engine: %w", err)
	}
	defer resp.Body.Close()

	return io.ReadAll(resp.Body)
}

// delete performs a DELETE request.
func (c *apiClient) delete(path string) error {
	resp, err := c.doRequest("DELETE", path, nil)
	if err != nil {
		return fmt.Errorf("cannot connect to engine: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusNoContent {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("API error (%d): %s", resp.StatusCode, string(body))
	}
	return nil
}

// post performs a POST request with JSON body and decodes response into dest.
func (c *apiClient) post(path string, body io.Reader, dest any) error {
	resp, err := c.doRequest("POST", path, body)
	if err != nil {
		return fmt.Errorf("cannot connect to engine: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		respBody, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("API error (%d): %s", resp.StatusCode, string(respBody))
	}

	if dest != nil {
		return json.NewDecoder(resp.Body).Decode(dest)
	}
	return nil
}

// patch performs a PATCH request with JSON body and decodes response into dest.
func (c *apiClient) patch(path string, body io.Reader, dest any) error {
	resp, err := c.doRequest("PATCH", path, body)
	if err != nil {
		return fmt.Errorf("cannot connect to engine: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("API error (%d): %s", resp.StatusCode, string(respBody))
	}

	if dest != nil {
		return json.NewDecoder(resp.Body).Decode(dest)
	}
	return nil
}
