// Theme: the HTTP API server (serve). Boots the server against the isolated
// home and exercises representative endpoints across the route table.
//go:build darwin

package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

func serveSuite() *Suite {
	const port = 17777
	base := fmt.Sprintf("http://127.0.0.1:%d", port)
	var server *background

	return &Suite{
		Name: "serve",
		Setup: func(h *Harness) error {
			bg, err := h.Start(nil, "serve", "--port", fmt.Sprintf("%d", port))
			if err != nil {
				return err
			}
			server = bg
			// Wait until the server answers.
			deadline := time.Now().Add(20 * time.Second)
			for time.Now().Before(deadline) {
				if _, _, err := httpGet(base + "/weave/host/status"); err == nil {
					return nil
				}
				time.Sleep(300 * time.Millisecond)
			}
			return fmt.Errorf("server did not become ready; output:\n%s", server.Output())
		},
		Teardown: func(h *Harness) {
			if server != nil {
				server.Stop()
			}
		},
		Cases: []Case{
			{"GET /weave/host/status returns host info", func(t *T, h *Harness) {
				status, body, err := httpGet(base + "/weave/host/status")
				if err != nil {
					t.Fatalf("request failed: %v", err)
				}
				if status != 200 {
					t.Fatalf("status = %d, want 200", status)
				}
				var host struct {
					CPUCount int `json:"cpuCount"`
				}
				if err := json.Unmarshal([]byte(body), &host); err != nil {
					t.Fatalf("parsing host status: %v\n%s", err, body)
				}
				if host.CPUCount <= 0 {
					t.Errorf("cpuCount = %d, want > 0", host.CPUCount)
				}
			}},
			{"GET /weave/vms returns a JSON array", func(t *T, h *Harness) {
				status, body, err := httpGet(base + "/weave/vms")
				if err != nil {
					t.Fatalf("request failed: %v", err)
				}
				if status != 200 {
					t.Fatalf("status = %d, want 200", status)
				}
				var vms []any
				if err := json.Unmarshal([]byte(body), &vms); err != nil {
					t.Fatalf("expected a JSON array: %v\n%s", err, body)
				}
			}},
			{"GET a missing VM returns 404", func(t *T, h *Harness) {
				status, _, err := httpGet(base + "/weave/vms/ghost")
				if err != nil {
					t.Fatalf("request failed: %v", err)
				}
				if status != 404 {
					t.Errorf("status = %d, want 404", status)
				}
			}},
			{"config storage location CRUD over HTTP", func(t *T, h *Harness) {
				status, _, err := httpPost(base+"/weave/config/locations",
					`{"name":"http-loc","path":"`+h.WeaveHome+`/http-loc"}`)
				if err != nil {
					t.Fatalf("POST failed: %v", err)
				}
				if status != 201 {
					t.Fatalf("POST status = %d, want 201", status)
				}
				_, body, err := httpGet(base + "/weave/config/locations")
				if err != nil {
					t.Fatalf("GET failed: %v", err)
				}
				if !strings.Contains(body, "http-loc") {
					t.Errorf("location absent after creation:\n%s", body)
				}
			}},
			{"GET /weave/logs returns text", func(t *T, h *Harness) {
				status, _, err := httpGet(base + "/weave/logs?type=info")
				if err != nil {
					t.Fatalf("request failed: %v", err)
				}
				if status != 200 {
					t.Errorf("status = %d, want 200", status)
				}
			}},
		},
	}
}

func httpGet(url string) (int, string, error) {
	client := &http.Client{Timeout: 10 * time.Second}
	response, err := client.Get(url)
	if err != nil {
		return 0, "", err
	}
	defer response.Body.Close()
	body, _ := io.ReadAll(response.Body)
	return response.StatusCode, string(body), nil
}

func httpPost(url, jsonBody string) (int, string, error) {
	client := &http.Client{Timeout: 10 * time.Second}
	response, err := client.Post(url, "application/json", strings.NewReader(jsonBody))
	if err != nil {
		return 0, "", err
	}
	defer response.Body.Close()
	body, _ := io.ReadAll(response.Body)
	return response.StatusCode, string(body), nil
}
