// Theme: the MCP stdio server (serve --mcp). Speaks JSON-RPC over the
// process's stdin/stdout, performs the initialize handshake and asserts the
// expected weave_* tools are advertised.
//go:build darwin

package main

import (
	"bufio"
	"encoding/json"
	"os/exec"
	"strings"
	"time"
)

func mcpSuite() *Suite {
	return &Suite{
		Name: "mcp",
		Cases: []Case{
			{"initialize and tools/list advertise the weave_ tools", func(t *T, h *Harness) {
				command := exec.Command(h.Bin, "serve", "--mcp")
				command.Env = h.env()
				stdin, err := command.StdinPipe()
				if err != nil {
					t.Fatalf("stdin pipe: %v", err)
				}
				stdout, err := command.StdoutPipe()
				if err != nil {
					t.Fatalf("stdout pipe: %v", err)
				}
				if err := command.Start(); err != nil {
					t.Fatalf("starting MCP server: %v", err)
				}
				defer func() {
					_ = command.Process.Kill()
					_ = command.Wait()
				}()

				requests := []string{
					`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"acc","version":"0"}}}`,
					`{"jsonrpc":"2.0","method":"notifications/initialized"}`,
					`{"jsonrpc":"2.0","id":2,"method":"tools/list"}`,
				}
				go func() {
					for _, request := range requests {
						_, _ = stdin.Write([]byte(request + "\n"))
						time.Sleep(50 * time.Millisecond)
					}
				}()

				tools := readToolNames(stdout, 10*time.Second)
				for _, want := range []string{
					"weave_list_vms", "weave_get_vm", "weave_run_vm", "weave_stop_vm",
					"weave_clone_vm", "weave_delete_vm", "weave_create_vm", "weave_exec",
				} {
					if !contains(tools, want) {
						t.Errorf("tool %q not advertised (got %v)", want, tools)
					}
				}
			}},
		},
	}
}

// readToolNames scans MCP responses for the tools/list result (id 2).
func readToolNames(stdout interface{ Read([]byte) (int, error) }, timeout time.Duration) []string {
	type rpcResponse struct {
		ID     int `json:"id"`
		Result struct {
			Tools []struct {
				Name string `json:"name"`
			} `json:"tools"`
		} `json:"result"`
	}

	done := make(chan []string, 1)
	go func() {
		scanner := bufio.NewScanner(stdout)
		scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
		for scanner.Scan() {
			line := strings.TrimSpace(scanner.Text())
			if line == "" {
				continue
			}
			var response rpcResponse
			if err := json.Unmarshal([]byte(line), &response); err != nil {
				continue
			}
			if response.ID == 2 {
				names := make([]string, 0, len(response.Result.Tools))
				for _, tool := range response.Result.Tools {
					names = append(names, tool.Name)
				}
				done <- names
				return
			}
		}
		_ = scanner.Err()
		done <- nil
	}()

	select {
	case names := <-done:
		return names
	case <-time.After(timeout):
		return nil
	}
}
