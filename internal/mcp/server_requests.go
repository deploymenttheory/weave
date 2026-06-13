// Port of lume's Server/Requests.swift and Responses.swift: the JSON bodies
// of the HTTP API server.
//go:build darwin

package mcp

// errorResponse is the uniform error body.
type errorResponse struct {
	Error string `json:"error"`
}

type createVMRequest struct {
	Name       string `json:"name"`
	FromIPSW   string `json:"fromIPSW,omitempty"` // url, path or "latest"; empty means latest for macOS
	Linux      bool   `json:"linux,omitempty"`
	DiskSizeGB uint16 `json:"diskSize,omitempty"` // default 50
}

type cloneVMRequest struct {
	Name    string `json:"name"`
	NewName string `json:"newName"`
}

type updateVMRequest struct {
	CPU        *uint16 `json:"cpu,omitempty"`
	MemoryMB   *uint64 `json:"memory,omitempty"`
	DiskSizeGB *uint16 `json:"diskSize,omitempty"`
	Display    *string `json:"display,omitempty"`
}

type runVMRequest struct {
	NoGraphics      bool     `json:"noGraphics,omitempty"`
	VNC             bool     `json:"vnc,omitempty"`
	VNCExperimental bool     `json:"vncExperimental,omitempty"`
	Recovery        bool     `json:"recoveryMode,omitempty"`
	SharedDirs      []string `json:"sharedDirectories,omitempty"` // --shared-dir syntax
	Dirs            []string `json:"dirs,omitempty"`              // --dir syntax
	Suspendable     bool     `json:"suspendable,omitempty"`
	Clipboard       bool     `json:"clipboard,omitempty"`
}

type stopVMRequest struct {
	Timeout uint64 `json:"timeout,omitempty"` // seconds, default 30
}

type pullVMRequest struct {
	Image       string `json:"image"` // remote name
	Concurrency uint   `json:"concurrency,omitempty"`
	Insecure    bool   `json:"insecure,omitempty"`
	Deduplicate bool   `json:"deduplicate,omitempty"`
}

type pushVMRequest struct {
	Name        string   `json:"name"`
	Images      []string `json:"images"` // remote names
	Concurrency uint     `json:"concurrency,omitempty"`
	Insecure    bool     `json:"insecure,omitempty"`
}

type pruneRequest struct {
	Entries     string `json:"entries,omitempty"` // caches or vms, default caches
	OlderThan   *uint  `json:"olderThan,omitempty"`
	SpaceBudget *uint  `json:"spaceBudget,omitempty"`
	GC          bool   `json:"gc,omitempty"`
}

type storageLocationRequest struct {
	Name string `json:"name"`
	Path string `json:"path"`
}

type configUpdateRequest struct {
	DefaultStorage *string `json:"defaultStorage,omitempty"`
	CacheDir       *string `json:"cacheDir,omitempty"`
	Registry       *struct {
		Host         string `json:"host,omitempty"`
		Organization string `json:"organization,omitempty"`
	} `json:"registry,omitempty"`
}

type pullJobResponse struct {
	ID        string `json:"id"`
	Image     string `json:"image"`
	Status    string `json:"status"` // running, succeeded or failed
	Error     string `json:"error,omitempty"`
	StartedAt string `json:"startedAt"`
	EndedAt   string `json:"endedAt,omitempty"`
}

type hostStatusResponse struct {
	Version     string `json:"version"`
	Model       string `json:"model,omitempty"`
	CPUCount    int    `json:"cpuCount"`
	MemoryBytes uint64 `json:"memoryBytes"`
}
