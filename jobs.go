package main

import (
	"fmt"
	"sync"
	"time"
)

type jobEvent struct {
	ID   int64  `json:"id"`
	Text string `json:"text"`
	At   string `json:"at"`
}

type job struct {
	mu       sync.Mutex
	snapshot JobSnapshot
	events   []jobEvent
	notify   chan struct{}
	done     bool
}

type jobStore struct {
	mu   sync.RWMutex
	jobs map[string]*job
}

func newJobStore() *jobStore { return &jobStore{jobs: make(map[string]*job)} }

func (s *jobStore) create(app, actor string) *job {
	id := fmt.Sprintf("%d", time.Now().UnixNano())
	j := &job{snapshot: JobSnapshot{ID: id, AppName: app, RequestedBy: actor, Status: JobPending, Message: "queued", CreatedAt: time.Now()}, notify: make(chan struct{})}
	s.mu.Lock()
	if len(s.jobs) >= 500 {
		var oldestID string
		var oldest time.Time
		for candidateID, candidate := range s.jobs {
			snapshot := candidate.view()
			if snapshot.FinishedAt == nil {
				continue
			}
			if oldestID == "" || snapshot.FinishedAt.Before(oldest) {
				oldestID, oldest = candidateID, *snapshot.FinishedAt
			}
		}
		if oldestID != "" {
			delete(s.jobs, oldestID)
		}
	}
	s.jobs[id] = j
	s.mu.Unlock()
	j.log("deployment queued")
	return j
}

func (s *jobStore) get(id string) *job {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.jobs[id]
}

func (j *job) update(status JobStatus, message string) {
	j.mu.Lock()
	j.snapshot.Status, j.snapshot.Message = status, message
	if status == JobSuccess || status == JobFailed || status == JobRolledBack {
		finished := time.Now()
		j.snapshot.FinishedAt, j.done = &finished, true
	}
	j.appendEventLocked(message)
	j.mu.Unlock()
}

func (j *job) log(message string) {
	j.mu.Lock()
	j.appendEventLocked(message)
	j.mu.Unlock()
}

func (j *job) appendEventLocked(message string) {
	id := int64(1)
	if len(j.events) > 0 {
		id = j.events[len(j.events)-1].ID + 1
	}
	j.events = append(j.events, jobEvent{ID: id, Text: message, At: time.Now().UTC().Format(time.RFC3339Nano)})
	if len(j.events) > 2000 {
		j.events = j.events[len(j.events)-2000:]
	}
	close(j.notify)
	j.notify = make(chan struct{})
}

func (j *job) Write(p []byte) (int, error) {
	for _, line := range splitLines(string(p)) {
		j.log(line)
	}
	return len(p), nil
}

func splitLines(s string) []string {
	lines := make([]string, 0, 1)
	for len(s) > 0 {
		i := 0
		for i < len(s) && s[i] != '\n' {
			i++
		}
		line := s[:i]
		if line != "" {
			lines = append(lines, line)
		}
		if i == len(s) {
			break
		}
		s = s[i+1:]
	}
	return lines
}

func (j *job) view() JobSnapshot { j.mu.Lock(); defer j.mu.Unlock(); return j.snapshot }
