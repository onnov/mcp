package jobs

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/onnov/mcp/internal/pc/workspace"
)

type persistedJob struct {
	ID               string    `json:"id"`
	Request          Request   `json:"request"`
	Status           string    `json:"status"`
	Error            string    `json:"error,omitempty"`
	ExitCode         *int      `json:"exit_code,omitempty"`
	Started          time.Time `json:"started"`
	Finished         time.Time `json:"finished"`
	Created          time.Time `json:"created"`
	CommandDirectory string    `json:"command_directory"`
	NetworkPolicy    string    `json:"network_policy"`
	Records          []Line    `json:"records"`
	TotalRecords     int       `json:"total_records"`
	Bytes            int64     `json:"bytes"`
}

func snapshotJob(j *job) persistedJob {
	rows, total, bytes := j.out.Snapshot()
	r := j.Request
	r.Args = append([]string(nil), r.Args...)
	code := j.ExitCode
	if code != nil {
		v := *code
		code = &v
	}
	return persistedJob{
		ID: j.ID, Request: r, Status: j.Status, Error: j.Error, ExitCode: code,
		Started: j.Started, Finished: j.Finished, Created: j.created,
		CommandDirectory: j.commandDirectory, NetworkPolicy: j.networkPolicy,
		Records: rows, TotalRecords: total, Bytes: bytes,
	}
}

func persistedOutput(p persistedJob, after int) OutputView {
	v := OutputView{
		Records: []Line{}, RecordsCursor: after, Head: []Line{}, Tail: []Line{},
		TotalRecords: p.TotalRecords, Bytes: p.Bytes, Cursor: after,
	}
	if len(p.Records) > 0 {
		v.Evicted = max(0, p.Records[0].Sequence-1-after)
	}
	recordBytes := 0
	for _, l := range p.Records {
		if l.Sequence <= after {
			continue
		}
		if len(v.Records) >= 200 || recordBytes+len(l.Text) > 128<<10 {
			break
		}
		v.Records = append(v.Records, l)
		v.RecordsCursor = l.Sequence
		recordBytes += len(l.Text)
	}
	v.More = v.RecordsCursor < p.TotalRecords
	for _, l := range p.Records {
		if len(v.Head) >= HeadLines {
			break
		}
		v.Head = append(v.Head, l)
	}
	if len(p.Records) > TailLines {
		v.Tail = append(v.Tail, p.Records[len(p.Records)-TailLines:]...)
	} else {
		v.Tail = append(v.Tail, p.Records...)
	}
	v.Omitted = max(0, p.TotalRecords-len(v.Head)-len(v.Tail))
	v.Cursor = p.TotalRecords
	if v.Evicted > 0 {
		v.Message = "Some persisted retained output was evicted before this cursor."
	}
	return v
}

func persistedView(p persistedJob, after int) View {
	r := p.Request
	r.Args = append([]string(nil), r.Args...)
	return View{
		ID: p.ID, Request: r, Status: p.Status, Error: p.Error, ExitCode: p.ExitCode,
		Started: p.Started, Finished: p.Finished, CommandDirectory: p.CommandDirectory,
		NetworkPolicy: p.NetworkPolicy, Output: persistedOutput(p, after),
	}
}

func (m *Manager) persistSnapshot(session string, p persistedJob) error {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	_, jobsDir, err := m.ws.ContextPathsForTarget(ctx, session, p.Request.Target)
	if err != nil {
		return err
	}
	data, err := json.MarshalIndent(p, "", "  ")
	if err != nil {
		return err
	}
	path := filepath.Join(jobsDir, p.ID+".json")
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0600); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		return err
	}
	return m.ws.PruneContext(ctx, session, p.Request.Target)
}

func readPersisted(path string) (persistedJob, error) {
	var p persistedJob
	data, err := os.ReadFile(path)
	if err != nil {
		return p, err
	}
	if err := json.Unmarshal(data, &p); err != nil {
		return p, err
	}
	return p, nil
}

func (m *Manager) loadPersisted(session string, t workspace.Target, id string) (persistedJob, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_, jobsDir, err := m.ws.ContextPaths(ctx, session, t)
	if err != nil {
		return persistedJob{}, err
	}
	return readPersisted(filepath.Join(jobsDir, id+".json"))
}

func (m *Manager) listPersisted(session string, t workspace.Target) ([]persistedJob, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_, jobsDir, err := m.ws.ContextPaths(ctx, session, t)
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(jobsDir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return []persistedJob{}, nil
		}
		return nil, err
	}
	rows := []persistedJob{}
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		p, err := readPersisted(filepath.Join(jobsDir, entry.Name()))
		if err == nil {
			rows = append(rows, p)
		}
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].Created.After(rows[j].Created) })
	if len(rows) > 50 {
		rows = rows[:50]
	}
	return rows, nil
}
