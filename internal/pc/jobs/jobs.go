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
	Args       []string `json:"args" jsonschema:"Executable and arguments, no implicit shell; use bash -lc explicitly if needed"`
	CWD        string   `json:"cwd,omitempty"`
	Purpose    string   `json:"purpose" jsonschema:"build, test, inspect, smoke, run, git or gh"`
	Network    bool     `json:"network,omitempty"`
	Seconds    int      `json:"seconds,omitempty"`
	Credential bool     `json:"credential,omitempty"`
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
}
type View struct {
	ExitCode *int       `json:"exit_code,omitempty"`
	ID       string     `json:"id"`
	Request  Request    `json:"request"`
	Status   string     `json:"status"`
	Error    string     `json:"error,omitempty"`
	Started  time.Time  `json:"started"`
	Finished time.Time  `json:"finished"`
	Output   OutputView `json:"output"`
}
type Manager struct {
	mu         sync.Mutex
	ctx        context.Context
	cancel     context.CancelFunc
	runner     sandbox.Runner
	ws         *workspace.Service
	jobs       map[string]*job
	maxSeconds int
	wg         sync.WaitGroup
}

func New(ctx context.Context, ws *workspace.Service, r sandbox.Runner, maxSeconds int) *Manager {
	ctx, cancel := context.WithCancel(ctx)
	return &Manager{ctx: ctx, cancel: cancel, runner: r, ws: ws, jobs: map[string]*job{}, maxSeconds: maxSeconds}
}
func (m *Manager) Close() { m.cancel(); m.wg.Wait() }
func id() string {
	var b [24]byte
	if _, e := rand.Read(b[:]); e != nil {
		panic(e)
	}
	return hex.EncodeToString(b[:])
}
func NeedsApproval(r Request) bool {
	return r.Purpose == "run" || r.Purpose == "smoke" || r.Purpose == "git" || r.Purpose == "gh" || r.Credential || r.Network
}
func (m *Manager) Prepare(r Request) (View, string, error) {
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
	if r.Credential && !r.Network {
		return View{}, "", errors.New("credential jobs require network:true")
	}
	if r.Credential && (len(r.Args) == 0 || !((r.Purpose == "gh" && r.Args[0] == "gh") || (r.Purpose == "git" && r.Args[0] == "git"))) {
		return View{}, "", errors.New("credentials are only passed to explicit git/gh jobs, with user approval")
	}
	if len(r.Args) == 0 || len(r.Args) > 128 {
		return View{}, "", errors.New("args must contain 1..128 items")
	}
	if r.Seconds < 1 || r.Seconds > m.maxSeconds {
		return View{}, "", fmt.Errorf("seconds must be 1..%d", m.maxSeconds)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
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
	j := &job{ID: id(), Request: r, Status: "queued", out: &Output{}, created: time.Now()}
	if NeedsApproval(r) {
		j.Status = "awaiting_approval"
		j.nonce = id()
	}
	m.jobs[j.ID] = j
	return m.view(j, 0), j.nonce, nil
}
func (m *Manager) Start(ctx context.Context, jobID, nonce string) (View, error) {
	m.mu.Lock()
	j := m.jobs[jobID]
	if j == nil {
		m.mu.Unlock()
		return View{}, errors.New("unknown job")
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
	spec := sandbox.Spec{Root: r, CWD: filepath.Join(prefix, j.Request.CWD), Args: j.Request.Args, Network: j.Request.Network, Seconds: j.Request.Seconds, Credential: j.Request.Credential}
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
		defer m.mu.Unlock()
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
	}()
	return v, nil
}
func (m *Manager) view(j *job, after int) View {
	return View{ID: j.ID, Request: j.Request, Status: j.Status, Error: j.Error, ExitCode: j.ExitCode, Started: j.Started, Finished: j.Finished, Output: j.out.View(after, j.Status != "running" && j.Status != "queued" && j.Status != "awaiting_approval")}
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
