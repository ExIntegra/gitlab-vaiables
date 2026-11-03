package app

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"gitlabvars/internal/demo"
	"gitlabvars/internal/gitlab"
)

func trackingFixture(t *testing.T, path, instance string) (*Server, http.Handler, *http.Cookie, string) {
	t.Helper()
	backend := demo.New()
	s := New(Config{Origin: "http://localhost:8080", GitLabURL: instance, SessionTTL: time.Hour, Demo: true}, backend, backend)
	if err := s.ConfigureTracking(path); err != nil {
		t.Fatal(err)
	}
	h := s.Handler()
	c, csrf := signIn(t, h)
	return s, h, c, csrf
}

func readTrackingState(t *testing.T, h http.Handler, c *http.Cookie) trackingState {
	t.Helper()
	w := call(h, "GET", "/api/tracking", "", "", c, "")
	if w.Code != 200 {
		t.Fatalf("GET tracking: %d %s", w.Code, w.Body)
	}
	if w.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("tracking response can be cached")
	}
	var state trackingState
	if err := json.Unmarshal(w.Body.Bytes(), &state); err != nil {
		t.Fatal(err)
	}
	if state.Sources == nil {
		t.Fatal("tracking sources must be an empty array, not null")
	}
	return state
}

func trackingPayload(sources []trackedSource, revision uint64) string {
	data, _ := json.Marshal(map[string]any{"sources": sources, "version": revision})
	return string(data)
}

func newTrackingIdentity(t *testing.T, s *Server, userID int64, isDemo bool) (*http.Cookie, string) {
	t.Helper()
	id, se, ok := s.sessions.create("isolated-test-token", gitlab.User{ID: userID, Name: "Private user name", Username: "private-user"}, isDemo)
	if !ok {
		t.Fatal("could not create test session")
	}
	return &http.Cookie{Name: cookieName, Value: id}, se.CSRF
}

func TestTrackingStartsEmptyAndPersistsAcrossLoginAndRestart(t *testing.T) {
	file := filepath.Join(t.TempDir(), "preferences", "tracking.json")
	s, h, c, csrf := trackingFixture(t, file, "https://gitlab.com")
	initial := readTrackingState(t, h, c)
	if len(initial.Sources) != 0 || initial.Version != 0 {
		t.Fatalf("new user follows sources by default: %+v", initial)
	}
	sources := []trackedSource{{Kind: "groups", ID: 201, Excluded: []trackedRef{{Kind: "projects", ID: 102}}}}
	w := call(h, "PUT", "/api/tracking", trackingPayload(sources, 0), csrf, c, s.cfg.Origin)
	if w.Code != 200 {
		t.Fatalf("save tracking: %d %s", w.Code, w.Body)
	}
	state := readTrackingState(t, h, c)
	if state.Version != 1 || !reflect.DeepEqual(state.Sources, sources) {
		t.Fatalf("saved preferences changed: %+v", state)
	}
	c2, _ := signIn(t, h)
	if got := readTrackingState(t, h, c2); !reflect.DeepEqual(got, state) {
		t.Fatalf("fresh login lost selections: %+v", got)
	}
	_, restarted, c3, _ := trackingFixture(t, file, "https://gitlab.com")
	if got := readTrackingState(t, restarted, c3); !reflect.DeepEqual(got, state) {
		t.Fatalf("restart lost selections: %+v", got)
	}
}

func TestTrackingRevisionConflictAndAtomicFileReplacement(t *testing.T) {
	file := filepath.Join(t.TempDir(), "tracking.json")
	s, h, c, csrf := trackingFixture(t, file, "https://gitlab.com")
	first := []trackedSource{{Kind: "groups", ID: 201, Excluded: []trackedRef{}}}
	second := []trackedSource{{Kind: "projects", ID: 101, Excluded: []trackedRef{}}}
	if w := call(h, "PUT", "/api/tracking", trackingPayload(first, 0), csrf, c, s.cfg.Origin); w.Code != 200 {
		t.Fatalf("first write: %d %s", w.Code, w.Body)
	}
	before, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	w := call(h, "PUT", "/api/tracking", trackingPayload(second, 0), csrf, c, s.cfg.Origin)
	if w.Code != 409 || !strings.Contains(w.Body.String(), "tracking_conflict") {
		t.Fatalf("stale tab overwrote preferences: %d %s", w.Code, w.Body)
	}
	after, err := os.ReadFile(file)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("conflict changed the saved file")
	}
	if w := call(h, "PUT", "/api/tracking", trackingPayload(second, 1), csrf, c, s.cfg.Origin); w.Code != 200 {
		t.Fatalf("replace existing file: %d %s", w.Code, w.Body)
	}
	_, restarted, c2, _ := trackingFixture(t, file, "https://gitlab.com")
	if got := readTrackingState(t, restarted, c2); got.Version != 2 || !reflect.DeepEqual(got.Sources, second) {
		t.Fatalf("replacement not durable: %+v", got)
	}
	leftovers, err := filepath.Glob(filepath.Join(filepath.Dir(file), ".tracking-*.tmp"))
	if err != nil || len(leftovers) != 0 {
		t.Fatalf("temporary files remain: %v %v", leftovers, err)
	}
}

func TestTrackingIsIsolatedByGitLabUserAndInstance(t *testing.T) {
	file := filepath.Join(t.TempDir(), "tracking.json")
	s, h, c, csrf := trackingFixture(t, file, "https://gitlab.com")
	first := []trackedSource{{Kind: "projects", ID: 101, Excluded: []trackedRef{}}}
	if w := call(h, "PUT", "/api/tracking", trackingPayload(first, 0), csrf, c, s.cfg.Origin); w.Code != 200 {
		t.Fatalf("save: %d %s", w.Code, w.Body)
	}
	otherUser, otherCSRF := newTrackingIdentity(t, s, 2, false)
	if got := readTrackingState(t, h, otherUser); len(got.Sources) != 0 || got.Version != 0 {
		t.Fatalf("another user inherited selections: %+v", got)
	}
	otherSources := []trackedSource{{Kind: "groups", ID: 202, Excluded: []trackedRef{}}}
	if w := call(h, "PUT", "/api/tracking", trackingPayload(otherSources, 0), otherCSRF, otherUser, s.cfg.Origin); w.Code != 200 {
		t.Fatalf("other user write: %d %s", w.Code, w.Body)
	}
	if got := readTrackingState(t, h, c); !reflect.DeepEqual(got.Sources, first) {
		t.Fatal("another user's write changed the first user's preferences")
	}
	instance, instanceHandler, instanceCookie, instanceCSRF := trackingFixture(t, file, "https://gitlab.example.com/team")
	if got := readTrackingState(t, instanceHandler, instanceCookie); len(got.Sources) != 0 || got.Version != 0 {
		t.Fatalf("another GitLab instance inherited selections: %+v", got)
	}
	if w := call(instanceHandler, "PUT", "/api/tracking", trackingPayload(otherSources, 0), instanceCSRF, instanceCookie, instance.cfg.Origin); w.Code != 200 {
		t.Fatalf("other instance write: %d %s", w.Code, w.Body)
	}
	_, reloaded, reloadedCookie, _ := trackingFixture(t, file, "https://gitlab.com/")
	if got := readTrackingState(t, reloaded, reloadedCookie); !reflect.DeepEqual(got.Sources, first) {
		t.Fatal("instance isolation or URL trailing-slash normalization failed")
	}
}

func TestTrackingDemoPreferencesNeverReachDiskOrLiveUsers(t *testing.T) {
	file := filepath.Join(t.TempDir(), "tracking.json")
	s, h, c, csrf := trackingFixture(t, file, "https://gitlab.com")
	liveSources := []trackedSource{{Kind: "projects", ID: 101, Excluded: []trackedRef{}}}
	if w := call(h, "PUT", "/api/tracking", trackingPayload(liveSources, 0), csrf, c, s.cfg.Origin); w.Code != 200 {
		t.Fatal(w.Code, w.Body)
	}
	before, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	demoCookie, demoCSRF := newTrackingIdentity(t, s, 1, true)
	if got := readTrackingState(t, h, demoCookie); len(got.Sources) != 0 || got.Version != 0 {
		t.Fatal("demo inherited real selections")
	}
	demoSources := []trackedSource{{Kind: "groups", ID: 201, Excluded: []trackedRef{}}}
	if w := call(h, "PUT", "/api/tracking", trackingPayload(demoSources, 0), demoCSRF, demoCookie, s.cfg.Origin); w.Code != 200 {
		t.Fatal(w.Code, w.Body)
	}
	after, err := os.ReadFile(file)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("demo changed the persistent tracking file")
	}
	if got := readTrackingState(t, h, c); !reflect.DeepEqual(got.Sources, liveSources) {
		t.Fatal("demo changed the live user's tracked sources")
	}
	restarted, restartedHandler, restartedCookie, _ := trackingFixture(t, file, "https://gitlab.com")
	newDemo, _ := newTrackingIdentity(t, restarted, 1, true)
	if got := readTrackingState(t, restartedHandler, newDemo); len(got.Sources) != 0 || got.Version != 0 {
		t.Fatal("demo selections survived server restart")
	}
	if got := readTrackingState(t, restartedHandler, restartedCookie); !reflect.DeepEqual(got.Sources, liveSources) {
		t.Fatal("live selections did not survive restart")
	}
}

func TestTrackingStorageContainsOnlyIdentifiersAndRevision(t *testing.T) {
	file := filepath.Join(t.TempDir(), "tracking.json")
	s, h, c, csrf := trackingFixture(t, file, "https://gitlab.com")
	sources := []trackedSource{{Kind: "groups", ID: 201, Excluded: []trackedRef{{Kind: "projects", ID: 102}}}}
	if w := call(h, "PUT", "/api/tracking", trackingPayload(sources, 0), csrf, c, s.cfg.Origin); w.Code != 200 {
		t.Fatal(w.Code, w.Body)
	}
	data, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"test-token", "csrf", "token", "username", "name", "value", "platform", "fictional", "gitlab.com"} {
		if bytes.Contains(data, []byte(forbidden)) {
			t.Fatalf("sensitive or unnecessary metadata in tracking file: %s", forbidden)
		}
	}
	var disk trackingDisk
	if err := json.Unmarshal(data, &disk); err != nil || disk.Schema != 1 || len(disk.Users) != 1 {
		t.Fatalf("invalid tracking file: %v", err)
	}
	for owner := range disk.Users {
		if len(owner) != 64 {
			t.Fatal("owner must be namespaced by an opaque identifier")
		}
	}
}

func TestTrackingRejectsMalformedSourcesWithoutChangingSelections(t *testing.T) {
	s, h, c, csrf := trackingFixture(t, "", "https://gitlab.com")
	tooMany := make([]trackedSource, 101)
	for i := range tooMany {
		tooMany[i] = trackedSource{Kind: "projects", ID: int64(i + 1)}
	}
	tooManyExcluded := make([]trackedRef, 1001)
	for i := range tooManyExcluded {
		tooManyExcluded[i] = trackedRef{Kind: "projects", ID: int64(i + 1)}
	}
	for name, payload := range map[string]string{
		"missing version":     `{"sources":[]}`,
		"negative version":    `{"sources":[],"version":-1}`,
		"null sources":        `{"sources":null,"version":0}`,
		"invalid kind":        trackingPayload([]trackedSource{{Kind: "instance", ID: 1}}, 0),
		"invalid ID":          trackingPayload([]trackedSource{{Kind: "projects", ID: 0}}, 0),
		"duplicate source":    trackingPayload([]trackedSource{{Kind: "projects", ID: 101}, {Kind: "projects", ID: 101}}, 0),
		"project exclusions":  trackingPayload([]trackedSource{{Kind: "projects", ID: 101, Excluded: []trackedRef{{Kind: "projects", ID: 102}}}}, 0),
		"self exclusion":      trackingPayload([]trackedSource{{Kind: "groups", ID: 201, Excluded: []trackedRef{{Kind: "groups", ID: 201}}}}, 0),
		"duplicate exclusion": trackingPayload([]trackedSource{{Kind: "groups", ID: 201, Excluded: []trackedRef{{Kind: "projects", ID: 101}, {Kind: "projects", ID: 101}}}}, 0),
		"invalid exclusion":   trackingPayload([]trackedSource{{Kind: "groups", ID: 201, Excluded: []trackedRef{{Kind: "projects", ID: -1}}}}, 0),
		"too many roots":      trackingPayload(tooMany, 0),
		"too many exclusions": trackingPayload([]trackedSource{{Kind: "groups", ID: 201, Excluded: tooManyExcluded}}, 0),
		"source name":         `{"sources":[{"kind":"projects","id":101,"name":"must not persist"}],"version":0}`,
		"source secret":       `{"sources":[{"kind":"projects","id":101,"value":"must not persist"}],"version":0}`,
		"top-level token":     `{"sources":[],"version":0,"token":"must not persist"}`,
	} {
		t.Run(name, func(t *testing.T) {
			w := call(h, "PUT", "/api/tracking", payload, csrf, c, s.cfg.Origin)
			if w.Code != 400 {
				t.Fatalf("malformed preferences accepted: %d %s", w.Code, w.Body)
			}
			if got := readTrackingState(t, h, c); got.Version != 0 || len(got.Sources) != 0 {
				t.Fatal("invalid request changed tracking")
			}
		})
	}
	if w := call(h, "PUT", "/api/tracking", trackingPayload([]trackedSource{}, 0), csrf, c, s.cfg.Origin); w.Code != 200 {
		t.Fatalf("explicit unfollow-all rejected: %d %s", w.Code, w.Body)
	}
}

func TestTrackingRequiresSessionOriginAndCSRF(t *testing.T) {
	s, h, c, csrf := trackingFixture(t, "", "https://gitlab.com")
	if w := call(h, "GET", "/api/tracking", "", "", nil, ""); w.Code != 401 {
		t.Fatalf("anonymous tracking read: %d", w.Code)
	}
	body := trackingPayload([]trackedSource{{Kind: "projects", ID: 101}}, 0)
	for _, input := range []struct {
		name, origin, csrf string
		cookie             *http.Cookie
		status             int
	}{
		{"anonymous", s.cfg.Origin, csrf, nil, 401},
		{"foreign origin", "https://evil.example", csrf, c, 403},
		{"missing origin", "", csrf, c, 403},
		{"missing CSRF", s.cfg.Origin, "", c, 403},
		{"wrong CSRF", s.cfg.Origin, "incorrect", c, 403},
	} {
		t.Run(input.name, func(t *testing.T) {
			w := call(h, "PUT", "/api/tracking", body, input.csrf, input.cookie, input.origin)
			if w.Code != input.status {
				t.Fatalf("got %d want %d", w.Code, input.status)
			}
		})
	}
	if got := readTrackingState(t, h, c); got.Version != 0 || len(got.Sources) != 0 {
		t.Fatal("unauthorized request changed selections")
	}
}

func TestTrackingConcurrentTabsDoNotLoseChanges(t *testing.T) {
	file := filepath.Join(t.TempDir(), "tracking.json")
	s, h, c, csrf := trackingFixture(t, file, "https://gitlab.com")
	start, result := make(chan struct{}), make(chan int, 2)
	for _, id := range []int64{101, 102} {
		go func() {
			<-start
			body := trackingPayload([]trackedSource{{Kind: "projects", ID: id}}, 0)
			result <- call(h, "PUT", "/api/tracking", body, csrf, c, s.cfg.Origin).Code
		}()
	}
	close(start)
	a, b := <-result, <-result
	if !((a == 200 && b == 409) || (a == 409 && b == 200)) {
		t.Fatalf("concurrent writes: %d, %d", a, b)
	}
	got := readTrackingState(t, h, c)
	if got.Version != 1 || len(got.Sources) != 1 {
		t.Fatalf("concurrent revisions corrupted: %+v", got)
	}
	_, restarted, c2, _ := trackingFixture(t, file, "https://gitlab.com")
	if reloaded := readTrackingState(t, restarted, c2); !reflect.DeepEqual(reloaded, got) {
		t.Fatal("persisted concurrent winner differs from in-memory winner")
	}
}

func TestTrackingCopiesReturnedAndSubmittedSlices(t *testing.T) {
	store := newTrackingStore()
	sources := []trackedSource{{Kind: "groups", ID: 201, Excluded: []trackedRef{{Kind: "projects", ID: 102}}}}
	written, err := store.put("owner", false, sources, 0)
	if err != nil {
		t.Fatal(err)
	}
	sources[0].ID = 999
	sources[0].Excluded[0].ID = 999
	written.Sources[0].ID = 888
	written.Sources[0].Excluded[0].ID = 888
	got := store.get("owner", false)
	if got.Sources[0].ID != 201 || got.Sources[0].Excluded[0].ID != 102 {
		t.Fatal("caller changed stored preferences through a shared slice")
	}
	got.Sources[0].Excluded[0].ID = 777
	if store.get("owner", false).Sources[0].Excluded[0].ID != 102 {
		t.Fatal("read result shares mutable storage")
	}
}

func TestTrackingRefusesCorruptFilesWithoutResettingThem(t *testing.T) {
	file := filepath.Join(t.TempDir(), "tracking.json")
	bad := []byte(`{"schema":1,"users":`)
	if err := os.WriteFile(file, bad, 0600); err != nil {
		t.Fatal(err)
	}
	s, _ := fixture()
	if err := s.ConfigureTracking(file); err == nil {
		t.Fatal("corrupt file was silently accepted")
	}
	unchanged, err := os.ReadFile(file)
	if err != nil || !bytes.Equal(unchanged, bad) {
		t.Fatal("corrupt file was overwritten instead of left for recovery")
	}
}

func TestTrackingFailedSaveDoesNotChangeInMemoryState(t *testing.T) {
	store := newTrackingStore()
	store.file = t.TempDir() // Existing directory cannot be replaced by a file.
	if _, err := store.put("owner", false, []trackedSource{{Kind: "projects", ID: 101}}, 0); err == nil {
		t.Fatal("saving over a directory unexpectedly succeeded")
	}
	if state := store.get("owner", false); state.Version != 0 || len(state.Sources) != 0 {
		t.Fatal("failed persistence committed preferences in memory")
	}
	if _, err := store.put("owner", false, []trackedSource{}, 1); !errors.Is(err, errTrackingConflict) {
		t.Fatalf("failed save advanced revision: %v", err)
	}
}

func TestProjectDetailLookupForTrackingHydration(t *testing.T) {
	_, h, c, _ := trackingFixture(t, "", "https://gitlab.com")
	w := call(h, "GET", "/api/projects/101", "", "", c, "")
	if w.Code != 200 {
		t.Fatalf("project hydration: %d %s", w.Code, w.Body)
	}
	var result struct {
		Project gitlab.Project `json:"project"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result.Project.ID != 101 || result.Project.PathWithNamespace != "platform/payments-api" {
		t.Fatalf("unexpected project: %+v", result.Project)
	}
	for _, input := range []struct {
		id     int64
		status int
	}{{0, 400}, {999, 404}} {
		if w := call(h, "GET", fmt.Sprintf("/api/projects/%d", input.id), "", "", c, ""); w.Code != input.status {
			t.Fatalf("project %d: %d", input.id, w.Code)
		}
	}
	if w := call(h, "GET", "/api/projects/101", "", "", nil, ""); w.Code != 401 {
		t.Fatalf("anonymous project lookup: %d", w.Code)
	}
}
