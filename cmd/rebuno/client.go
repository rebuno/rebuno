package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/rebuno/rebuno/internal/domain"
)

const defaultTimeout = 30 * time.Second

var kernelBaseURL string

type client struct {
	baseURL string
	http    *http.Client
}

func newClient(baseURL string) *client {
	return &client{
		baseURL: strings.TrimSuffix(baseURL, "/"),
		http:    &http.Client{Timeout: defaultTimeout},
	}
}

func kernelClient() *client { return newClient(kernelBaseURL) }

func kernelURL() string {
	if v := os.Getenv("REBUNO_URL"); v != "" {
		return v
	}
	return "http://localhost:8080"
}

func (c *client) do(ctx context.Context, method, path string, body, out any) error {
	var payload io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		payload = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, payload)
	if err != nil {
		return err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if key := os.Getenv("REBUNO_API_KEY"); key != "" {
		req.Header.Set("Authorization", "Bearer "+key)
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return responseError(resp)
	}
	if out == nil || resp.StatusCode == http.StatusNoContent {
		return nil
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

func responseError(resp *http.Response) error {
	var apiErr domain.APIError
	if err := json.NewDecoder(resp.Body).Decode(&apiErr); err == nil && apiErr.Message != "" {
		return fmt.Errorf("%s: %s", resp.Status, apiErr.Message)
	}
	return fmt.Errorf("%s", resp.Status)
}
