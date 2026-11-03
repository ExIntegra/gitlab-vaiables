package app

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"

	"gitlabvars/internal/gitlab"
)

const maxTrackingBytes = 8 << 20

var errTrackingConflict = errors.New("tracking version conflict")

type trackedRef struct {
	Kind string `json:"kind"`
	ID   int64  `json:"id"`
}

type trackedSource struct {
	Kind     string       `json:"kind"`
	ID       int64        `json:"id"`
	Excluded []trackedRef `json:"excluded"`
}

type trackingState struct {
	Sources []trackedSource `json:"sources"`
	Version uint64          `json:"version"`
}

type trackingDisk struct {
	Schema int                      `json:"schema"`
	Users  map[string]trackingState `json:"users"`
}

type trackingStore struct {
	mu    sync.RWMutex
	file  string
	users map[string]trackingState
	demo  map[string]trackingState
}

func newTrackingStore() *trackingStore {
	return &trackingStore{users: make(map[string]trackingState), demo: make(map[string]trackingState)}
}

// ConfigureTracking is called before serving requests. Only source identifiers
// are persisted. Tokens, names and variable values never enter this file.
func (s *Server) ConfigureTracking(path string) error {
	if path == "" {
		return nil
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return err
	}
	if err = os.MkdirAll(filepath.Dir(abs), 0700); err != nil {
		return fmt.Errorf("create tracking directory: %w", err)
	}
	store := newTrackingStore()
	store.file = abs
	file, err := os.Open(abs)
	if errors.Is(err, os.ErrNotExist) {
		s.tracking = store
		return nil
	}
	if err != nil {
		return fmt.Errorf("open tracking file: %w", err)
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, maxTrackingBytes+1))
	if err != nil || len(data) > maxTrackingBytes {
		return errors.New("tracking file is unreadable or too large")
	}
	var disk trackingDisk
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&disk) != nil || decoder.Decode(new(any)) != io.EOF || disk.Schema != 1 || disk.Users == nil || len(disk.Users) > 4096 {
		return errors.New("invalid tracking file; restore a valid backup rather than resetting preferences")
	}
	for owner, state := range disk.Users {
		if len(owner) != 64 || validateSources(state.Sources) != nil {
			return errors.New("invalid tracking record")
		}
		disk.Users[owner] = cloneTracking(state)
	}
	store.users = disk.Users
	s.tracking = store
	return nil
}

func cloneTracking(state trackingState) trackingState {
	result := trackingState{Sources: make([]trackedSource, 0, len(state.Sources)), Version: state.Version}
	for _, source := range state.Sources {
		source.Excluded = append([]trackedRef{}, source.Excluded...)
		result.Sources = append(result.Sources, source)
	}
	return result
}

func validRef(kind string, id int64) bool { return (kind == "projects" || kind == "groups") && id > 0 }

func validateSources(sources []trackedSource) error {
	if sources == nil || len(sources) > 100 {
		return errors.New("choose no more than 100 sources")
	}
	roots := make(map[trackedRef]bool)
	totalExcluded := 0
	for _, source := range sources {
		ref := trackedRef{source.Kind, source.ID}
		if !validRef(ref.Kind, ref.ID) || roots[ref] || (source.Kind == "projects" && len(source.Excluded) > 0) {
			return errors.New("invalid or repeated source")
		}
		roots[ref] = true
		totalExcluded += len(source.Excluded)
		if len(source.Excluded) > 1000 || totalExcluded > 2000 {
			return errors.New("too many excluded sources")
		}
		seen := make(map[trackedRef]bool)
		for _, excluded := range source.Excluded {
			if !validRef(excluded.Kind, excluded.ID) || seen[excluded] || excluded == ref {
				return errors.New("invalid or repeated exclusion")
			}
			seen[excluded] = true
		}
	}
	return nil
}

func (s *Server) trackingOwner(se session) string {
	namespace := strings.TrimRight(s.cfg.GitLabURL, "/") + "\x00" + strconv.FormatInt(se.User.ID, 10)
	hash := sha256.Sum256([]byte(namespace))
	return hex.EncodeToString(hash[:])
}

func (s *trackingStore) get(owner string, demo bool) trackingState {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if demo {
		return cloneTracking(s.demo[owner])
	}
	return cloneTracking(s.users[owner])
}

func (s *trackingStore) put(owner string, demo bool, sources []trackedSource, expected uint64) (trackingState, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	records := s.users
	if demo {
		records = s.demo
	}
	if records[owner].Version != expected {
		return trackingState{}, errTrackingConflict
	}
	if len(records) >= 4096 {
		if _, ok := records[owner]; !ok {
			return trackingState{}, errors.New("tracking capacity reached")
		}
	}
	next := cloneTracking(trackingState{Sources: sources, Version: expected + 1})
	if next.Version == 0 {
		return trackingState{}, errors.New("tracking revision limit reached")
	}
	if !demo && s.file != "" {
		snapshot := make(map[string]trackingState, len(records)+1)
		for key, value := range records {
			snapshot[key] = value
		}
		snapshot[owner] = next
		data, err := json.Marshal(trackingDisk{Schema: 1, Users: snapshot})
		if err != nil || len(data) > maxTrackingBytes {
			return trackingState{}, errors.New("tracking file capacity reached")
		}
		if err = atomicTrackingWrite(s.file, data); err != nil {
			return trackingState{}, err
		}
	}
	records[owner] = next
	return cloneTracking(next), nil
}

func atomicTrackingWrite(path string, data []byte) error {
	file, err := os.CreateTemp(filepath.Dir(path), ".tracking-*.tmp")
	if err != nil {
		return err
	}
	temp := file.Name()
	defer os.Remove(temp)
	if _, err = file.Write(data); err != nil {
		file.Close()
		return err
	}
	if err = file.Sync(); err != nil {
		file.Close()
		return err
	}
	if err = file.Close(); err != nil {
		return err
	}
	return os.Rename(temp, path)
}

func (s *Server) getTracking(w http.ResponseWriter, r *http.Request, se session) {
	writeJSON(w, 200, s.tracking.get(s.trackingOwner(se), se.Demo))
}

func (s *Server) putTracking(w http.ResponseWriter, r *http.Request, se session) {
	var input struct {
		Sources []trackedSource `json:"sources"`
		Version *uint64         `json:"version"`
	}
	if !readJSON(w, r, &input) {
		return
	}
	if input.Version == nil || validateSources(input.Sources) != nil {
		fail(w, 400, "tracking_validation", "Проверьте выбранные источники. Допускается до 100 проектов или групп без повторов.")
		return
	}
	next, err := s.tracking.put(s.trackingOwner(se), se.Demo, input.Sources, *input.Version)
	if errors.Is(err, errTrackingConflict) {
		fail(w, 409, "tracking_conflict", "Список отслеживания изменён в другой вкладке. Обновите его перед повторной попыткой.")
		return
	}
	if err != nil {
		fail(w, 503, "tracking_unavailable", "Не удалось сохранить список отслеживания. Проверьте доступность хранилища сервера.")
		return
	}
	writeJSON(w, 200, next)
}

func (s *Server) project(w http.ResponseWriter, r *http.Request, se session) {
	id, ok := projectID(w, r)
	if !ok {
		return
	}
	b, ok := s.backend(se).(interface {
		Project(context.Context, string, int64) (gitlab.Project, error)
	})
	if !ok {
		fail(w, 503, "unavailable", "Проект временно недоступен.")
		return
	}
	project, err := b.Project(r.Context(), se.Token, id)
	if err != nil {
		s.upstreamError(w, r, err)
		return
	}
	writeJSON(w, 200, map[string]any{"project": project})
}
