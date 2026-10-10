// Package jobs owns bounded asynchronous jobs. State survives chats, not restarts.
package jobs

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/onnov/mcp/internal/pc/sandbox"
	"github.com/onnov/mcp/internal/pc/workspace"
)

type Request struct {
	workspace.Target
	Args        []string `json:"args" jsonschema:"Executable and arguments, no implicit shell; use bash -lc explicitly if needed"`
	CWD         string   `json:"cwd,omitempty"`
	Purpose     string   `json:"purpose" jsonschema:"build, test, inspect, smoke, run, git or gh"`
	Network     bool     `json:"network,omitempty"`
	HostNetwork bool     `json:"host_network,omitempty" jsonschema:"Request direct host network including localhost/LAN; requires operator allow-host-network and confirmation"`
	Seconds     int      `json:"seconds,omitempty"`
	Credential  bool     `json:"credential,omitempty"`
}
type job struct {
	ID                string
	Request           Request
	Status            string
	Error             string
	ExitCode          *int
	Started, Finished time.Time
	out               *Output
	cancel            context.CancelFunc
	nonce             string
	created           time.Time
	commandDirectory  string
	networkPolicy     string
	session           string
}
type View struct {
	ExitCode         *int       `json:"exit_code,omitempty"`
	ID               string     `json:"id"`
	Request          Request    `json:"request"`
	Status           string     `json:"status"`
	Error            string     `json:"error,omitempty"`
	Started          time.Time  `json:"started"`
	Finished         time.Time  `json:"finished"`
	Output           OutputView `json:"output"`
	CommandDirectory string     `json:"command_directory"`
	NetworkPolicy    string     `json:"network_policy"`
}
type Manager struct {
	mu              sync.Mutex
	ctx             context.Context
	cancel          context.CancelFunc
	runner          sandbox.Runner
	ws              *workspace.Service
	jobs            map[string]*job
	maxSeconds      int
	wg              sync.WaitGroup
	closed          bool
	ConfirmCommands bool
	MaxActive       int
}

func New(ctx context.Context, ws *workspace.Service, r sandbox.Runner, maxSeconds int) *Manager {
	ctx, cancel := context.WithCancel(ctx)
	return &Manager{ctx: ctx, cancel: cancel, runner: r, ws: ws, jobs: map[string]*job{}, maxSeconds: maxSeconds, MaxActive: 4}
}
func (m *Manager) Close() {
	if stopper, ok := m.runner.(interface{ StopAll() }); ok {
		stopper.StopAll()
	}
	m.mu.Lock()
	m.closed = true
	m.cancel()
	m.mu.Unlock()
	m.wg.Wait()
}
func id() string {
	var b [24]byte
	if _, e := rand.Read(b[:]); e != nil {
		panic(e)
	}
	return hex.EncodeToString(b[:])
}
func NeedsApproval(r Request) bool {
	// Purpose describes intent; it does not reduce a command's capabilities.
	// The operator grants ordinary RW development execution to this single-owner plugin.
	return r.Credential || r.Network || r.HostNetwork
}
func allowedCredentialGit(args []string) bool {
	if len(args) < 2 || args[0] != "git" {
		return false
	}
	switch args[1] {
	case "pull", "push", "fetch", "clone", "ls-remote":
		return true
	default:
		return false
	}
}

func (m *Manager) NeedsApproval(r Request) bool { return m.ConfirmCommands || NeedsApproval(r) }
func (m *Manager) Prepare(r Request) (View, string, error) {
	return m.prepare("", r, false)
}
func (m *Manager) PrepareApproval(r Request) (View, string, error) {
	return m.prepare("", r, true)
}
func (m *Manager) PrepareSession(session string, r Request) (View, string, error) {
	return m.prepare(session, r, false)
}
func (m *Manager) PrepareApprovalSession(session string, r Request) (View, string, error) {
	return m.prepare(session, r, true)
}
func (m *Manager) prepare(session string, r Request, forceApproval bool) (View, string, error) {
	if r.CWD == "" {
		r.CWD = "."
	}
	if r.Seconds == 0 {
		r.Seconds = min(300, m.maxSeconds)
	}
	switch r.Purpose {
	case "build", "test", "inspect", "smoke", "run", "git", "gh":
	default:
		return View{}, "", errors.New("invalid purpose")
	}
	if !filepath.IsLocal(r.CWD) {
		return View{}, "", errors.New("cwd must be relative inside the selected directory")
	}
	total := 0
	for _, a := range r.Args {
		total += len(a)
		if strings.IndexByte(a, 0) >= 0 {
			return View{}, "", errors.New("NUL in argument")
		}
	}
	if total > 65536 {
		return View{}, "", errors.New("arguments exceed 64 KiB")
	}
	if r.Directory == "" {
		return View{}, "", errors.New("explicit directory required")
	}
	if r.HostNetwork && (!r.Network || r.Credential) {
		return View{}, "", errors.New("host_network requires network:true and credential:false; credential jobs use the isolated proxy")
	}
	if r.Credential && !r.Network {
		return View{}, "", errors.New("credential jobs require network:true")
	}
	if r.Credential && (len(r.Args) == 0 || (r.Args[0] != "gh" && r.Args[0] != "git")) {
		return View{}, "", errors.New("credentials are only passed to explicit git/gh jobs, with user approval")
	}
	if r.Credential && r.Args[0] == "git" && !allowedCredentialGit(r.Args) {
		return View{}, "", errors.New("credential git jobs are limited to pull, push, fetch, clone and ls-remote")
	}
	if len(r.Args) == 0 || len(r.Args) > 128 || r.Args[0] == "" {
		return View{}, "", errors.New("args must contain 1..128 items")
	}
	if r.Seconds < 1 || r.Seconds > m.maxSeconds {
		return View{}, "", fmt.Errorf("seconds must be 1..%d", m.maxSeconds)
	}
	if err := m.ws.ValidateSessionTarget(session, r.Target); err != nil {
		return View{}, "", err
	}
	boundary, err := m.ws.CommandBoundary(r.Target)
	if err != nil {
		return View{}, "", err
	}
	policy := "none"
	if r.Network {
		policy = "public-proxy"
		if provider, ok := m.runner.(interface{ NetworkPolicy() string }); ok {
			policy = provider.NetworkPolicy()
		}
		if r.HostNetwork {
			policy = "host"
		}
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return View{}, "", errors.New("job manager is closed")
	}
	r.Args = append([]string(nil), r.Args...)
	if len(m.jobs) >= 50 {
		var oldest *job
		for _, j := range m.jobs {
			if j.Status != "running" && (oldest == nil || j.created.Before(oldest.created)) {
				oldest = j
			}
		}
		if oldest != nil {
			delete(m.jobs, oldest.ID)
		} else {
			return View{}, "", errors.New("job history full")
		}
	}
	j := &job{ID: id(), Request: r, Status: "queued", out: &Output{}, created: time.Now(), commandDirectory: boundary, networkPolicy: policy, session: session}
	if forceApproval || m.NeedsApproval(r) {
		j.Status = "awaiting_approval"
		j.nonce = id()
	}
	m.jobs[j.ID] = j
	return m.view(j, 0), j.nonce, nil
}
func (m *Manager) Start(ctx context.Context, jobID, nonce string) (View, error) {
	return m.start(ctx, "", jobID, nonce)
}
func (m *Manager) StartSession(ctx context.Context, session, jobID, nonce string) (View, error) {
	return m.start(ctx, session, jobID, nonce)
}
func (m *Manager) start(ctx context.Context, session, jobID, nonce string) (View, error) {
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return View{}, errors.New("job manager is closed")
	}
	active := 0
	for _, row := range m.jobs {
		if row.Status == "running" {
			active++
		}
	}
	maximum := m.MaxActive
	if maximum == 0 {
		maximum = 4
	}
	if active >= maximum {
		m.mu.Unlock()
		return View{}, errors.New("active job limit reached; stop a job before starting another")
	}
	j := m.jobs[jobID]
	if j == nil {
		m.mu.Unlock()
		return View{}, errors.New("unknown job")
	}
	if j.session != session {
		m.mu.Unlock()
		return View{}, errors.New("job belongs to another chat session")
	}
	if j.Status != "queued" && j.Status != "awaiting_approval" {
		m.mu.Unlock()
		return View{}, errors.New("job was already started or cancelled")
	}
	if j.Status == "awaiting_approval" && (nonce == "" || nonce != j.nonce) {
		m.mu.Unlock()
		return View{}, errors.New("approval must come from the confirmation card")
	}
	if time.Since(j.created) > 10*time.Minute {
		j.Status = "expired"
		m.mu.Unlock()
		return View{}, errors.New("approval expired; prepare the command again")
	}
	r, prefix, release, e := m.ws.ReserveCommand(ctx, j.Request.Target)
	if e != nil {
		m.mu.Unlock()
		return View{}, e
	}
	if m.ws.RootDirectory(r) != j.commandDirectory {
		release()
		m.mu.Unlock()
		return View{}, errors.New("command checkout boundary changed; prepare it again")
	}
	spec := sandbox.Spec{Root: r, CWD: filepath.Join(prefix, j.Request.CWD), Args: j.Request.Args, Network: j.Request.Network, HostNetwork: j.Request.HostNetwork, Seconds: j.Request.Seconds, Credential: j.Request.Credential}
	if e = sandbox.Validate(spec, m.maxSeconds); e != nil {
		release()
		m.mu.Unlock()
		return View{}, e
	}
	jobCtx, cancel := context.WithCancel(m.ctx)
	j.cancel = cancel
	j.nonce = ""
	j.Status = "running"
	j.Started = time.Now()
	v := m.view(j, 0)
	m.wg.Add(1)
	m.mu.Unlock()
	go func() {
		defer m.wg.Done()
		defer cancel()
		out, er := j.out.Writer("stdout"), j.out.Writer("stderr")
		e := m.runner.Run(jobCtx, spec, out, er)
		out.Close()
		er.Close()
		release()
		m.mu.Lock()
		j.Finished = time.Now()
		j.Status = "succeeded"
		code := 0
		j.ExitCode = &code
		if e != nil {
			j.Status = "failed"
			j.ExitCode = nil
			if exit, ok := e.(interface{ ExitCode() int }); ok {
				code := exit.ExitCode()
				j.ExitCode = &code
			}
			j.Error = e.Error()
			if errors.Is(e, context.Canceled) {
				j.Status = "cancelled"
			}
			if errors.Is(e, context.DeadlineExceeded) {
				j.Status = "timed_out"
			}
		}
		p := snapshotJob(j)
		jobSession := j.session
		m.mu.Unlock()
		_ = m.persistSnapshot(jobSession, p)
	}()
	return v, nil
}
func (m *Manager) view(j *job, after int) View {
	r := j.Request
	r.Args = append([]string(nil), r.Args...)
	return View{ID: j.ID, Request: r, CommandDirectory: j.commandDirectory, NetworkPolicy: j.networkPolicy, Status: j.Status, Error: j.Error, ExitCode: j.ExitCode, Started: j.Started, Finished: j.Finished, Output: j.out.View(after, j.Status != "running" && j.Status != "queued" && j.Status != "awaiting_approval")}
}
func (m *Manager) Get(jobID string, after int) (View, error) {
	if after < 0 {
		return View{}, errors.New("negative output cursor")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	j := m.jobs[jobID]
	if j == nil {
		return View{}, errors.New("unknown job")
	}
	return m.view(j, after), nil
}
func (m *Manager) List() []View {
	m.mu.Lock()
	defer m.mu.Unlock()
	v := []View{}
	rows := []*job{}
	for _, j := range m.jobs {
		rows = append(rows, j)
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].created.After(rows[j].created) })
	for _, j := range rows {
		summary := m.view(j, int(^uint(0)>>1))
		summary.Output.Cursor = 0
		v = append(v, summary)
	}
	return v
}
func (m *Manager) Cancel(jobID string) (View, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	j := m.jobs[jobID]
	if j == nil {
		return View{}, errors.New("unknown job")
	}
	if j.Status == "running" {
		j.cancel()
	} else if j.Status == "queued" || j.Status == "awaiting_approval" {
		j.Status = "cancelled"
		j.nonce = ""
		j.Finished = time.Now()
	}
	return m.view(j, 0), nil
}

// CancelAll cancels running commands and invalidates every pending approval.
// The manager remains usable; callers poll the returned IDs until terminal.
func (m *Manager) CancelAll() []View {
	if stopper, ok := m.runner.(interface{ StopAll() }); ok {
		stopper.StopAll()
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	rows := []View{}
	for _, j := range m.jobs {
		if j.Status == "running" {
			j.cancel()
			rows = append(rows, m.view(j, 0))
		} else if j.Status == "queued" || j.Status == "awaiting_approval" {
			j.Status = "cancelled"
			j.nonce = ""
			j.Finished = time.Now()
			rows = append(rows, m.view(j, 0))
		}
	}
	return rows
}

func (m *Manager) Output(jobID string, after, limit int) (OutputPage, error) {
	if after < 0 {
		return OutputPage{}, errors.New("negative output cursor")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	j := m.jobs[jobID]
	if j == nil {
		return OutputPage{}, errors.New("unknown job")
	}
	return j.out.Page(after, limit), nil
}
