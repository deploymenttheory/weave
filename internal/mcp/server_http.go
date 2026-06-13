// Port of lume's Server/Server.swift + Handlers.swift on net/http: a REST
// API for the VM lifecycle, served under /weave/*. The route table mirrors
// lume's /lume/* one. The run endpoint spawns a detached "weave run"
// subprocess because run owns a main thread and an AppKit run loop; async
// pulls return 202 with a job id that can be polled.
//go:build darwin

package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"runtime"
	"strconv"
	"sync"
	"syscall"
	"time"

	"github.com/deploymenttheory/weave/internal/ci"
	weavecommand "github.com/deploymenttheory/weave/internal/command"
	weaveconfig "github.com/deploymenttheory/weave/internal/config"
	"github.com/deploymenttheory/weave/internal/diskimage"
	weaveerrors "github.com/deploymenttheory/weave/internal/errors"
	"github.com/deploymenttheory/weave/internal/logging"
	"github.com/deploymenttheory/weave/internal/objcutil"
	"github.com/deploymenttheory/weave/internal/oci"
	"github.com/deploymenttheory/weave/internal/unattended"

	"golang.org/x/sys/unix"
)

// APIServer hosts the REST API.
type APIServer struct {
	port uint16
	pull pullJobs
}

func NewAPIServer(port uint16) *APIServer {
	return &APIServer{port: port}
}

// pullJobs tracks asynchronous pulls in memory (lume's pull tracker).
type pullJobs struct {
	mutex sync.Mutex
	next  int
	jobs  map[string]*pullJob
}

type pullJob struct {
	id        string
	image     string
	status    string
	err       string
	startedAt time.Time
	endedAt   time.Time
}

func (p *pullJobs) start(image string, run func() error) string {
	p.mutex.Lock()
	defer p.mutex.Unlock()
	if p.jobs == nil {
		p.jobs = map[string]*pullJob{}
	}
	p.next++
	job := &pullJob{
		id:        strconv.Itoa(p.next),
		image:     image,
		status:    "running",
		startedAt: time.Now(),
	}
	p.jobs[job.id] = job

	go func() {
		err := run()
		p.mutex.Lock()
		defer p.mutex.Unlock()
		job.endedAt = time.Now()
		if err != nil {
			job.status = "failed"
			job.err = err.Error()
		} else {
			job.status = "succeeded"
		}
	}()
	return job.id
}

func (p *pullJobs) get(id string) (pullJobResponse, bool) {
	p.mutex.Lock()
	defer p.mutex.Unlock()
	job, ok := p.jobs[id]
	if !ok {
		return pullJobResponse{}, false
	}
	response := pullJobResponse{
		ID:        job.id,
		Image:     job.image,
		Status:    job.status,
		Error:     job.err,
		StartedAt: job.startedAt.UTC().Format(time.RFC3339),
	}
	if !job.endedAt.IsZero() {
		response.EndedAt = job.endedAt.UTC().Format(time.RFC3339)
	}
	return response, true
}

// Run serves until ctx is cancelled.
func (s *APIServer) Run(ctx context.Context) error {
	mux := http.NewServeMux()
	s.routes(mux)

	server := &http.Server{
		Addr:    fmt.Sprintf("127.0.0.1:%d", s.port),
		Handler: mux,
	}

	listener, err := net.Listen("tcp", server.Addr)
	if err != nil {
		if errors.Is(err, syscall.EADDRINUSE) {
			return weaveerrors.ErrGeneric("port %d is already in use, try --port %d", s.port, s.port+1)
		}
		return err
	}

	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdownCtx)
	}()

	fmt.Printf("Serving HTTP API on http://%s\n", server.Addr)
	logging.LogInfo("HTTP API server started on %s", server.Addr)

	if err := server.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

func (s *APIServer) routes(mux *http.ServeMux) {
	mux.HandleFunc("GET /weave/vms", s.handleListVMs)
	mux.HandleFunc("POST /weave/vms", s.handleCreateVM)
	mux.HandleFunc("GET /weave/vms/{name}", s.handleGetVM)
	mux.HandleFunc("DELETE /weave/vms/{name}", s.handleDeleteVM)
	mux.HandleFunc("PATCH /weave/vms/{name}", s.handleUpdateVM)
	mux.HandleFunc("POST /weave/vms/clone", s.handleCloneVM)
	mux.HandleFunc("POST /weave/vms/push", s.handlePushVM)
	mux.HandleFunc("POST /weave/vms/{name}/run", s.handleRunVM)
	mux.HandleFunc("POST /weave/vms/{name}/stop", s.handleStopVM)
	mux.HandleFunc("POST /weave/vms/{name}/setup", s.handleSetupVM)
	mux.HandleFunc("GET /weave/ipsw", s.handleIPSW)
	mux.HandleFunc("POST /weave/pull", s.handlePull)
	mux.HandleFunc("POST /weave/pull/start", s.handlePullStart)
	mux.HandleFunc("GET /weave/pull/{id}", s.handlePullStatus)
	mux.HandleFunc("POST /weave/prune", s.handlePrune)
	mux.HandleFunc("GET /weave/images", s.handleImages)
	mux.HandleFunc("GET /weave/config", s.handleGetConfig)
	mux.HandleFunc("POST /weave/config", s.handleUpdateConfig)
	mux.HandleFunc("GET /weave/config/locations", s.handleListLocations)
	mux.HandleFunc("POST /weave/config/locations", s.handleAddLocation)
	mux.HandleFunc("DELETE /weave/config/locations/{name}", s.handleRemoveLocation)
	mux.HandleFunc("POST /weave/config/locations/default/{name}", s.handleDefaultLocation)
	mux.HandleFunc("GET /weave/logs", s.handleLogs)
	mux.HandleFunc("GET /weave/host/status", s.handleHostStatus)
}

// ---------------------------------------------------------------------------
// JSON plumbing
// ---------------------------------------------------------------------------

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func writeError(w http.ResponseWriter, err error) {
	status := http.StatusInternalServerError
	var vmErr *weaveerrors.VMError
	if errors.As(err, &vmErr) && vmErr.Kind == weaveerrors.VMErrorNotFound {
		status = http.StatusNotFound
	}
	var usageErr *weaveerrors.UsageError
	if errors.As(err, &usageErr) {
		status = http.StatusBadRequest
	}
	logging.LogError("API error: %v", err)
	writeJSON(w, status, errorResponse{Error: err.Error()})
}

// readJSON decodes the request body into T; a fully empty body yields the
// zero value.
func readJSON[T any](w http.ResponseWriter, r *http.Request) (T, bool) {
	var value T
	if r.Body == nil {
		return value, true
	}
	err := json.NewDecoder(r.Body).Decode(&value)
	if err != nil && !errors.Is(err, io.EOF) {
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: "invalid JSON body: " + err.Error()})
		return value, false
	}
	return value, true
}

// ---------------------------------------------------------------------------
// Handlers
// ---------------------------------------------------------------------------

func (s *APIServer) handleListVMs(w http.ResponseWriter, r *http.Request) {
	infos, err := collectVMInfos(r.URL.Query().Get("source"))
	if err != nil {
		writeError(w, err)
		return
	}
	if infos == nil {
		infos = []weavecommand.ListVMInfo{}
	}
	writeJSON(w, http.StatusOK, infos)
}

func (s *APIServer) handleGetVM(w http.ResponseWriter, r *http.Request) {
	details, err := collectVMDetails(r.Context(), r.PathValue("name"))
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, details)
}

func (s *APIServer) handleCreateVM(w http.ResponseWriter, r *http.Request) {
	request, ok := readJSON[createVMRequest](w, r)
	if !ok {
		return
	}
	if request.Name == "" {
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: "name is required"})
		return
	}
	command := &weavecommand.CreateCommand{Name: request.Name, Linux: request.Linux, DiskSize: 50, DiskFormat: diskimage.DiskImageFormatRaw}
	if request.DiskSizeGB != 0 {
		command.DiskSize = request.DiskSizeGB
	}
	if !request.Linux {
		command.FromIPSW = request.FromIPSW
		if command.FromIPSW == "" {
			command.FromIPSW = "latest"
		}
	}
	if err := command.Run(r.Context()); err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]string{"name": request.Name})
}

func (s *APIServer) handleDeleteVM(w http.ResponseWriter, r *http.Request) {
	command := &weavecommand.DeleteCommand{Names: []string{r.PathValue("name")}}
	if err := command.Run(r.Context()); err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"deleted": r.PathValue("name")})
}

func (s *APIServer) handleUpdateVM(w http.ResponseWriter, r *http.Request) {
	request, ok := readJSON[updateVMRequest](w, r)
	if !ok {
		return
	}
	command := &weavecommand.SetCommand{Name: r.PathValue("name")}
	command.CPU = request.CPU
	command.Memory = request.MemoryMB
	command.DiskSize = request.DiskSizeGB
	if request.Display != nil {
		displayConfig := weavecommand.ParseVMDisplayConfig(*request.Display)
		command.Display = &displayConfig
	}
	if err := command.Run(r.Context()); err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"updated": r.PathValue("name")})
}

func (s *APIServer) handleCloneVM(w http.ResponseWriter, r *http.Request) {
	request, ok := readJSON[cloneVMRequest](w, r)
	if !ok {
		return
	}
	if request.Name == "" || request.NewName == "" {
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: "name and newName are required"})
		return
	}
	command := &weavecommand.CloneCommand{SourceName: request.Name, NewName: request.NewName, Concurrency: 4, PruneLimit: 100}
	if err := command.Run(r.Context()); err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]string{"name": request.NewName})
}

func (s *APIServer) handleRunVM(w http.ResponseWriter, r *http.Request) {
	request, ok := readJSON[runVMRequest](w, r)
	if !ok {
		return
	}
	name := r.PathValue("name")

	args := []string{}
	// A server-started VM defaults to headless unless graphics requested.
	if request.NoGraphics || (!request.VNC && !request.VNCExperimental) {
		args = append(args, "--no-graphics")
	}
	if request.VNC {
		args = append(args, "--vnc")
	}
	if request.VNCExperimental {
		args = append(args, "--vnc-experimental")
	}
	if request.Recovery {
		args = append(args, "--recovery")
	}
	if request.Suspendable {
		args = append(args, "--suspendable")
	}
	if request.Clipboard {
		args = append(args, "--clipboard")
	}
	for _, dir := range request.SharedDirs {
		args = append(args, "--shared-dir", dir)
	}
	for _, dir := range request.Dirs {
		args = append(args, "--dir", dir)
	}

	if err := spawnDetachedRun(name, args); err != nil {
		writeError(w, err)
		return
	}
	if err := waitForVMRunning(r.Context(), name, 30*time.Second); err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"running": name})
}

func (s *APIServer) handleStopVM(w http.ResponseWriter, r *http.Request) {
	request, ok := readJSON[stopVMRequest](w, r)
	if !ok {
		return
	}
	timeout := request.Timeout
	if timeout == 0 {
		timeout = 30
	}
	command := &weavecommand.StopCommand{Name: r.PathValue("name"), Timeout: timeout}
	if err := command.Run(r.Context()); err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"stopped": r.PathValue("name")})
}

// handleSetupVM runs preset-mode unattended setup synchronously (long
// request — lume's handler behaves the same way).
func (s *APIServer) handleSetupVM(w http.ResponseWriter, r *http.Request) {
	request, ok := readJSON[struct {
		Unattended string `json:"unattended"`
	}](w, r)
	if !ok {
		return
	}
	if request.Unattended == "" {
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: "unattended preset name or path is required"})
		return
	}
	config, err := unattended.LoadUnattendedConfig(request.Unattended)
	if err != nil {
		writeError(w, err)
		return
	}
	if err := unattended.RunUnattendedSetup(r.Context(), unattended.SetupOptions{Name: r.PathValue("name")}, config); err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"setup": r.PathValue("name")})
}

func (s *APIServer) handleIPSW(w http.ResponseWriter, r *http.Request) {
	image, err := weavecommand.FetchLatestSupportedRestoreImage(r.Context())
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"url": objcutil.GoStr(image.URL().AbsoluteString())})
}

func (s *APIServer) pullCommandFrom(request pullVMRequest) *weavecommand.PullCommand {
	concurrency := request.Concurrency
	if concurrency == 0 {
		concurrency = 4
	}
	return &weavecommand.PullCommand{
		RemoteName:  request.Image,
		Insecure:    request.Insecure,
		Concurrency: concurrency,
		Deduplicate: request.Deduplicate,
	}
}

func (s *APIServer) handlePull(w http.ResponseWriter, r *http.Request) {
	request, ok := readJSON[pullVMRequest](w, r)
	if !ok {
		return
	}
	if request.Image == "" {
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: "image is required"})
		return
	}
	if err := s.pullCommandFrom(request).Run(r.Context()); err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"pulled": request.Image})
}

func (s *APIServer) handlePullStart(w http.ResponseWriter, r *http.Request) {
	request, ok := readJSON[pullVMRequest](w, r)
	if !ok {
		return
	}
	if request.Image == "" {
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: "image is required"})
		return
	}
	command := s.pullCommandFrom(request)
	id := s.pull.start(request.Image, func() error {
		return command.Run(context.Background())
	})
	writeJSON(w, http.StatusAccepted, map[string]string{"id": id})
}

func (s *APIServer) handlePullStatus(w http.ResponseWriter, r *http.Request) {
	job, ok := s.pull.get(r.PathValue("id"))
	if !ok {
		writeJSON(w, http.StatusNotFound, errorResponse{Error: "no such pull job"})
		return
	}
	writeJSON(w, http.StatusOK, job)
}

func (s *APIServer) handlePushVM(w http.ResponseWriter, r *http.Request) {
	request, ok := readJSON[pushVMRequest](w, r)
	if !ok {
		return
	}
	if request.Name == "" || len(request.Images) == 0 {
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: "name and images are required"})
		return
	}
	concurrency := request.Concurrency
	if concurrency == 0 {
		concurrency = 4
	}
	command := &weavecommand.PushCommand{
		LocalName:   request.Name,
		RemoteNames: request.Images,
		Insecure:    request.Insecure,
		Concurrency: concurrency,
	}
	if err := command.Run(r.Context()); err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"pushed": request.Name})
}

func (s *APIServer) handlePrune(w http.ResponseWriter, r *http.Request) {
	request, ok := readJSON[pruneRequest](w, r)
	if !ok {
		return
	}
	entries := request.Entries
	if entries == "" {
		entries = "caches"
	}
	command := &weavecommand.PruneCommand{
		Entries:     entries,
		OlderThan:   request.OlderThan,
		SpaceBudget: request.SpaceBudget,
		GC:          request.GC,
	}
	if err := command.Validate(); err != nil {
		writeError(w, err)
		return
	}
	if err := command.Run(); err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"pruned": entries})
}

func (s *APIServer) handleImages(w http.ResponseWriter, r *http.Request) {
	repository := r.URL.Query().Get("repository")
	if repository == "" {
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: "repository query parameter is required"})
		return
	}
	command := &weavecommand.ImagesCommand{Repository: repository}
	remoteName, err := oci.NewRemoteName(repository + ":latest")
	if err != nil {
		writeError(w, weaveerrors.ErrFailedToParseRemoteName(err.Error()))
		return
	}
	registry, err := oci.NewRegistry(remoteName.Host, remoteName.Namespace, command.Insecure, nil)
	if err != nil {
		writeError(w, err)
		return
	}
	tags, err := registry.TagsList(r.Context())
	if err != nil {
		writeError(w, err)
		return
	}
	if tags == nil {
		tags = []string{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"repository": repository, "tags": tags})
}

func (s *APIServer) handleGetConfig(w http.ResponseWriter, r *http.Request) {
	settings, err := weaveconfig.LoadSettings()
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, settings)
}

func (s *APIServer) handleUpdateConfig(w http.ResponseWriter, r *http.Request) {
	request, ok := readJSON[configUpdateRequest](w, r)
	if !ok {
		return
	}
	settings, err := weaveconfig.LoadSettings()
	if err != nil {
		writeError(w, err)
		return
	}
	if request.DefaultStorage != nil {
		settings.DefaultStorage = *request.DefaultStorage
	}
	if request.CacheDir != nil {
		settings.CacheDir = *request.CacheDir
	}
	if request.Registry != nil {
		settings.Registry = &weaveconfig.RegistrySettings{
			Host:         request.Registry.Host,
			Organization: request.Registry.Organization,
		}
	}
	if err := settings.Save(); err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, settings)
}

func (s *APIServer) handleListLocations(w http.ResponseWriter, r *http.Request) {
	settings, err := weaveconfig.LoadSettings()
	if err != nil {
		writeError(w, err)
		return
	}
	locations := settings.StorageLocations
	if locations == nil {
		locations = map[string]string{}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"locations": locations,
		"default":   settings.DefaultStorage,
	})
}

func (s *APIServer) handleAddLocation(w http.ResponseWriter, r *http.Request) {
	request, ok := readJSON[storageLocationRequest](w, r)
	if !ok {
		return
	}
	if request.Name == "" || request.Path == "" {
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: "name and path are required"})
		return
	}
	if !weaveconfig.StorageLocationNamePattern.MatchString(request.Name) {
		writeError(w, weaveerrors.ErrInvalidStorageLocation(request.Name))
		return
	}
	path := objcutil.ExpandTilde(request.Path)
	if err := weaveconfig.ValidateStorageLocation(path); err != nil {
		writeError(w, err)
		return
	}
	settings, err := weaveconfig.LoadSettings()
	if err != nil {
		writeError(w, err)
		return
	}
	if settings.StorageLocations == nil {
		settings.StorageLocations = map[string]string{}
	}
	settings.StorageLocations[request.Name] = path
	if err := settings.Save(); err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]string{request.Name: path})
}

func (s *APIServer) handleRemoveLocation(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	settings, err := weaveconfig.LoadSettings()
	if err != nil {
		writeError(w, err)
		return
	}
	if _, ok := settings.StorageLocations[name]; !ok {
		writeError(w, weaveerrors.ErrStorageLocationNotFound(name))
		return
	}
	delete(settings.StorageLocations, name)
	if settings.DefaultStorage == name {
		settings.DefaultStorage = ""
	}
	if err := settings.Save(); err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"removed": name})
}

func (s *APIServer) handleDefaultLocation(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	settings, err := weaveconfig.LoadSettings()
	if err != nil {
		writeError(w, err)
		return
	}
	path, err := settings.ResolveStorageLocation(name)
	if err != nil {
		writeError(w, err)
		return
	}
	if err := weaveconfig.ValidateStorageLocation(path); err != nil {
		writeError(w, err)
		return
	}
	settings.DefaultStorage = name
	if err := settings.Save(); err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"default": name})
}

func (s *APIServer) handleLogs(w http.ResponseWriter, r *http.Request) {
	logType := r.URL.Query().Get("type")
	if logType == "" {
		logType = "all"
	}
	lines := 0
	if rawLines := r.URL.Query().Get("lines"); rawLines != "" {
		parsed, err := strconv.Atoi(rawLines)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, errorResponse{Error: "invalid lines parameter"})
			return
		}
		lines = parsed
	}
	command := &weavecommand.LogsCommand{Type: logType, Lines: lines}
	if err := command.Validate(); err != nil {
		writeError(w, err)
		return
	}

	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	for _, file := range command.LogFiles() {
		_ = weavecommand.WriteTail(w, file.Path, file.Prefix, lines)
	}
}

func (s *APIServer) handleHostStatus(w http.ResponseWriter, r *http.Request) {
	memory, _ := unix.SysctlUint64("hw.memsize")
	model, _ := syscall.Sysctl("hw.model")
	writeJSON(w, http.StatusOK, hostStatusResponse{
		Version:     ci.CIVersion(),
		Model:       model,
		CPUCount:    runtime.NumCPU(),
		MemoryBytes: memory,
	})
}
