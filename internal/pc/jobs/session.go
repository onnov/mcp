package jobs

import (
	"errors"
	"sort"
	"time"

	"github.com/onnov/mcp/internal/pc/workspace"
)

func (m *Manager) GetSession(session string, target workspace.Target, jobID string, after int) (View, error) {
	if after < 0 {
		return View{}, errors.New("negative output cursor")
	}
	m.mu.Lock()
	if j := m.jobs[jobID]; j != nil {
		if j.session != session {
			m.mu.Unlock()
			return View{}, errors.New("job belongs to another chat session")
		}
		v := m.view(j, after)
		m.mu.Unlock()
		return v, nil
	}
	m.mu.Unlock()
	p, err := m.loadPersisted(session, target, jobID)
	if err != nil {
		return View{}, errors.New("unknown job")
	}
	return persistedView(p, after), nil
}

func (m *Manager) ListSession(session string, target workspace.Target) ([]View, error) {
	type row struct {
		created int64
		view    View
	}
	merged := map[string]row{}

	m.mu.Lock()
	for _, j := range m.jobs {
		if j.session != session {
			continue
		}
		summary := m.view(j, int(^uint(0)>>1))
		summary.Output.Cursor = 0
		merged[j.ID] = row{created: j.created.UnixNano(), view: summary}
	}
	m.mu.Unlock()

	persisted, err := m.listPersisted(session, target)
	if err != nil {
		return nil, err
	}
	for _, p := range persisted {
		if _, ok := merged[p.ID]; ok {
			continue
		}
		v := persistedView(p, int(^uint(0)>>1))
		v.Output.Records = []Line{}
		v.Output.RecordsCursor = 0
		v.Output.Cursor = 0
		v.Output.More = false
		merged[p.ID] = row{created: p.Created.UnixNano(), view: v}
	}
	rows := make([]row, 0, len(merged))
	for _, r := range merged {
		rows = append(rows, r)
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].created > rows[j].created })
	if len(rows) > 50 {
		rows = rows[:50]
	}
	out := make([]View, 0, len(rows))
	for _, r := range rows {
		out = append(out, r.view)
	}
	return out, nil
}

func (m *Manager) CancelSession(session, jobID string) (View, error) {
	m.mu.Lock()
	j := m.jobs[jobID]
	if j == nil {
		m.mu.Unlock()
		return View{}, errors.New("unknown or already persisted job")
	}
	if j.session != session {
		m.mu.Unlock()
		return View{}, errors.New("job belongs to another chat session")
	}
	persist := false
	if j.Status == "running" {
		j.cancel()
	} else if j.Status == "queued" || j.Status == "awaiting_approval" {
		j.Status = "cancelled"
		j.nonce = ""
		j.Finished = time.Now().UTC()
		persist = true
	}
	v := m.view(j, 0)
	var p persistedJob
	if persist {
		p = snapshotJob(j)
	}
	m.mu.Unlock()
	if persist {
		_ = m.persistSnapshot(session, p)
	}
	return v, nil
}

func (m *Manager) OutputSession(session string, target workspace.Target, jobID string, after, limit int) (OutputPage, error) {
	if after < 0 {
		return OutputPage{}, errors.New("negative output cursor")
	}
	m.mu.Lock()
	if j := m.jobs[jobID]; j != nil {
		if j.session != session {
			m.mu.Unlock()
			return OutputPage{}, errors.New("job belongs to another chat session")
		}
		page := j.out.Page(after, limit)
		m.mu.Unlock()
		return page, nil
	}
	m.mu.Unlock()
	p, err := m.loadPersisted(session, target, jobID)
	if err != nil {
		return OutputPage{}, errors.New("unknown job")
	}
	if limit < 1 || limit > 200 {
		limit = 100
	}
	out := OutputPage{Records: []Line{}, Cursor: after, TotalRecords: p.TotalRecords}
	if len(p.Records) > 0 {
		out.Evicted = max(0, p.Records[0].Sequence-1-after)
	}
	bytes := 0
	for _, l := range p.Records {
		if l.Sequence <= after {
			continue
		}
		if len(out.Records) >= limit || bytes+len(l.Text) > 128<<10 {
			break
		}
		out.Records = append(out.Records, l)
		out.Cursor = l.Sequence
		bytes += len(l.Text)
	}
	return out, nil
}

func (m *Manager) CancelAllSession(session string) []View {
	if session == "" {
		return m.CancelAll()
	}
	m.mu.Lock()
	rows := []View{}
	type pendingPersist struct {
		session string
		job     persistedJob
	}
	persist := []pendingPersist{}
	for _, j := range m.jobs {
		if j.session != session {
			continue
		}
		if j.Status == "running" {
			j.cancel()
			rows = append(rows, m.view(j, 0))
		} else if j.Status == "queued" || j.Status == "awaiting_approval" {
			j.Status = "cancelled"
			j.nonce = ""
			j.Finished = time.Now().UTC()
			rows = append(rows, m.view(j, 0))
			persist = append(persist, pendingPersist{session: session, job: snapshotJob(j)})
		}
	}
	m.mu.Unlock()
	for _, p := range persist {
		_ = m.persistSnapshot(p.session, p.job)
	}
	return rows
}
