package api

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	log "github.com/sirupsen/logrus"
	"gopkg.in/yaml.v3"

	"github.com/natesales/pathvector/pkg/bird"
	"github.com/natesales/pathvector/pkg/config"
	"github.com/natesales/pathvector/pkg/process"
	"github.com/natesales/pathvector/pkg/report"
)

// Server is the pathvector management API. Mutations are serialized and run
// through the same load -> render -> validate -> apply pipeline as `generate`.
type Server struct {
	ConfigFile  string
	SessionsDir string
	Version     string
	Commit      string
	Date        string

	mu  sync.Mutex // serializes mutations and applies
	mux *http.ServeMux

	lastApply    time.Time // last successful apply
	lastApplyErr string    // last apply error, if any
}

var (
	sessionNameRegex = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9_.-]*$`)
	sensitiveRegex   = regexp.MustCompile(`(?m)^(\s*)(password|api-key|peeringdb-api-key|report-key):.*$`)
)

// New builds a Server for the given config file. Session fragments are stored
// in sessionsDir (default: sessions.d next to the config file).
func New(configFile, sessionsDir, version, commit, date string) *Server {
	if sessionsDir == "" {
		sessionsDir = filepath.Join(filepath.Dir(configFile), "sessions.d")
	}
	s := &Server{
		ConfigFile:  configFile,
		SessionsDir: sessionsDir,
		Version:     version,
		Commit:      commit,
		Date:        date,
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/health", s.health)
	mux.HandleFunc("GET /v1/version", s.versionInfo)
	mux.HandleFunc("GET /v1/status", s.status)
	mux.HandleFunc("GET /v1/sessions", s.sessions)
	mux.HandleFunc("GET /v1/sessions/{name}", s.getSession)
	mux.HandleFunc("GET /v1/sessions/{name}/config", s.getSessionConfig)
	mux.HandleFunc("PUT /v1/sessions/{name}", s.putSession)
	mux.HandleFunc("PATCH /v1/sessions/{name}", s.patchSession)
	mux.HandleFunc("POST /v1/sessions/{name}/enable", s.setSessionDisabled(false))
	mux.HandleFunc("POST /v1/sessions/{name}/disable", s.setSessionDisabled(true))
	mux.HandleFunc("DELETE /v1/sessions/{name}", s.deleteSession)
	mux.HandleFunc("POST /v1/reconcile", s.reconcile)
	mux.HandleFunc("GET /v1/rules", s.getRules)
	mux.HandleFunc("PUT /v1/rules", s.putRules)
	mux.HandleFunc("DELETE /v1/rules", s.deleteRules)
	mux.HandleFunc("GET /v1/config", s.getConfig)
	mux.HandleFunc("PUT /v1/config", s.putConfig)
	mux.HandleFunc("POST /v1/generate", s.generate)
	mux.HandleFunc("POST /v1/bird", s.birdCommand)
	mux.HandleFunc("POST /v1/report", s.reportNow)
	s.mux = mux
	return s
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, code int, err error) {
	writeJSON(w, code, map[string]string{"error": err.Error()})
}

// recoverer turns handler panics into 500s instead of killing the connection
func recoverer(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if rec := recover(); rec != nil {
				writeErr(w, http.StatusInternalServerError, fmt.Errorf("internal error: %v", rec))
			}
		}()
		h.ServeHTTP(w, r)
	})
}

// auth wraps the mux, requiring a bearer token when api-key is configured
func (s *Server) auth(key string, h http.Handler) http.Handler {
	if key == "" {
		return h
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token := ""
		if auth := r.Header.Get("Authorization"); strings.HasPrefix(auth, "Bearer ") {
			token = strings.TrimPrefix(auth, "Bearer ")
		} else if k := r.Header.Get("X-API-Key"); k != "" {
			token = k
		} else {
			token = r.URL.Query().Get("key")
		}
		if token != key {
			writeErr(w, http.StatusForbidden, fmt.Errorf("bad token"))
			return
		}
		h.ServeHTTP(w, r)
	})
}

// ListenAndServe starts the API on listen ("unix:///path" or host:port) and
// returns the listener address actually bound.
func (s *Server) ListenAndServe(listen, key string) error {
	var listener net.Listener
	var err error
	if strings.HasPrefix(listen, "unix://") {
		sockPath := strings.TrimPrefix(listen, "unix://")
		if err := os.MkdirAll(filepath.Dir(sockPath), 0755); err != nil {
			return err
		}
		if err := os.Remove(sockPath); err != nil && !os.IsNotExist(err) {
			return err
		}
		listener, err = net.Listen("unix", sockPath)
		if err == nil {
			//nolint:golint,gosec
			_ = os.Chmod(sockPath, 0660)
		}
	} else {
		listener, err = net.Listen("tcp", listen)
		if key == "" {
			listener.Close()
			return fmt.Errorf("refusing to listen on %s without api-key set (use unix:// or set api-key)", listen)
		}
	}
	if err != nil {
		return err
	}
	log.Infof("API listening on %s", listen)
	srv := &http.Server{
		Handler:           s.auth(key, recoverer(s.mux)),
		ReadHeaderTimeout: 10 * time.Second,
	}
	return srv.Serve(listener)
}

func (s *Server) load() (*config.Config, error) {
	return process.LoadFile(s.ConfigFile)
}

// birdTimeout resolves the BIRD command timeout for a loaded config
func birdTimeout(c *config.Config) time.Duration {
	if c != nil && c.BIRDTimeout > 0 {
		return time.Duration(c.BIRDTimeout) * time.Second
	}
	return bird.DefaultCommandTimeout
}

// reqOpts extracts the common mutation query flags: ?dry_run=1 renders and
// validates without applying; ?skip_pdb=1 and ?skip_irr=1 run the apply while
// skipping PeeringDB/bgpq4 lookups (useful when those services are down)
func reqOpts(r *http.Request) (dryRun, skipPDB, skipIRR bool) {
	q := r.URL.Query()
	return q.Get("dry_run") == "1", q.Get("skip_pdb") == "1", q.Get("skip_irr") == "1"
}

// apply runs the full generate pipeline against the on-disk config and records
// the outcome for /v1/status. Callers must hold s.mu.
func (s *Server) apply(dryRun, skipPDB, skipIRR bool) error {
	err := process.Run(s.ConfigFile, "", s.Version, process.RunOptions{
		DryRun:  dryRun,
		SkipPDB: skipPDB,
		SkipIRR: skipIRR,
	})
	if err != nil {
		s.lastApplyErr = err.Error()
	} else {
		s.lastApply = time.Now()
		s.lastApplyErr = ""
	}
	return err
}

// configSHA256 hashes the base config plus all session fragment files, so API
// clients can detect drift
func (s *Server) configSHA256() string {
	h := sha256.New()
	if b, err := os.ReadFile(s.ConfigFile); err == nil {
		h.Write(b)
	}
	frags, err := filepath.Glob(filepath.Join(s.sessionsDir(), "*.yml"))
	if err == nil {
		sort.Strings(frags)
		for _, f := range frags {
			if b, err := os.ReadFile(f); err == nil {
				h.Write(b)
			}
		}
	}
	return fmt.Sprintf("%x", h.Sum(nil))
}

// ---- read endpoints ----

func (s *Server) health(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) versionInfo(w http.ResponseWriter, r *http.Request) {
	out := map[string]any{"version": s.Version, "commit": s.Commit, "date": s.Date}
	if c, err := s.load(); err == nil {
		_, v, err := bird.RunCommand("", c.BIRDSocket, birdTimeout(c))
		if err == nil {
			out["bird"] = v
		}
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) status(w http.ResponseWriter, r *http.Request) {
	c, err := s.load()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	resp, _, err := bird.RunCommand("show protocols all", c.BIRDSocket, birdTimeout(c))
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	states, err := bird.ParseProtocols(resp)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	out := map[string]any{"protocols": states, "config_sha256": s.configSHA256()}
	if !s.lastApply.IsZero() {
		out["last_apply"] = s.lastApply.UTC().Format(time.RFC3339)
	}
	if s.lastApplyErr != "" {
		out["last_apply_error"] = s.lastApplyErr
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) sessions(w http.ResponseWriter, r *http.Request) {
	c, err := s.load()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	rep, err := report.Collect(c, c.ReportRouter, 0)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, rep)
}

func (s *Server) getSession(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	c, err := s.load()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	prefixCap := c.ReportPrefixCap
	if v := r.URL.Query().Get("prefixes"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			prefixCap = n
		}
	}
	rep, err := report.Collect(c, c.ReportRouter, prefixCap)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	for _, sess := range rep.Sessions {
		if sess.Name == name {
			writeJSON(w, http.StatusOK, sess)
			return
		}
	}
	writeErr(w, http.StatusNotFound, fmt.Errorf("session %q not found", name))
}

// getSessionConfig returns the API-managed session fragment with sensitive
// fields redacted, so clients can verify desired state without pulling the
// whole config
func (s *Server) getSessionConfig(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	_, fields, err := s.readFragment(s.sessionsDir(), name)
	if err != nil {
		writeErr(w, http.StatusNotFound, fmt.Errorf("session %q is not API-managed", name))
		return
	}
	b, err := yaml.Marshal(map[string]any{name: fields})
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	sanitized := sensitiveRegex.ReplaceAll(b, []byte("${1}${2}: REDACTED"))
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	_, _ = w.Write(sanitized)
}

// ---- mutations ----

func validSessionName(name string) bool {
	return sessionNameRegex.MatchString(name) && !strings.Contains(name, "..")
}

// sessionsDir resolves the fragment directory from the on-disk config each
// call, so edits to api-sessions-dir take effect without restarting serve.
// It parses just that one key (non-strict) so it still works when the config
// is broken elsewhere - e.g. DELETE must be able to remove a bad fragment.
// Relative paths resolve against the config file's directory, same as
// include: globs.
func (s *Server) sessionsDir() string {
	var minimal struct {
		Dir string `yaml:"api-sessions-dir"`
	}
	if b, err := os.ReadFile(s.ConfigFile); err == nil {
		_ = yaml.Unmarshal(b, &minimal)
	}
	if minimal.Dir == "" {
		return s.SessionsDir
	}
	if !filepath.IsAbs(minimal.Dir) {
		return filepath.Join(filepath.Dir(s.ConfigFile), minimal.Dir)
	}
	return minimal.Dir
}

func (s *Server) fragmentPath(dir, name string) string {
	return filepath.Join(dir, name+".yml")
}

// writeFragment renders a session fragment file {peers: {name: fields}}
func (s *Server) writeFragment(dir, name string, fields map[string]any) error {
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}
	doc := map[string]any{"peers": map[string]any{name: fields}}
	b, err := yaml.Marshal(doc)
	if err != nil {
		return err
	}
	header := []byte("# Managed by pathvector API - do not edit\n")
	return os.WriteFile(s.fragmentPath(dir, name), append(header, b...), 0644)
}

func (s *Server) readFragment(dir, name string) ([]byte, map[string]any, error) {
	b, err := os.ReadFile(s.fragmentPath(dir, name))
	if err != nil {
		return nil, nil, err
	}
	var doc map[string]any
	if err := yaml.Unmarshal(b, &doc); err != nil {
		return b, nil, err
	}
	fields, _ := doc["peers"].(map[string]any)[name].(map[string]any)
	return b, fields, nil
}

func (s *Server) putSession(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if !validSessionName(name) {
		writeErr(w, http.StatusBadRequest, fmt.Errorf("invalid session name %q", name))
		return
	}
	var fields map[string]any
	if err := json.NewDecoder(r.Body).Decode(&fields); err != nil || len(fields) == 0 {
		writeErr(w, http.StatusBadRequest, fmt.Errorf("invalid JSON body: %v", err))
		return
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	dir := s.sessionsDir()
	old, readErr := os.ReadFile(s.fragmentPath(dir, name))
	existed := readErr == nil

	if err := s.writeFragment(dir, name, fields); err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	if _, err := s.load(); err != nil {
		s.rollbackFragment(dir, name, old, existed)
		writeErr(w, http.StatusUnprocessableEntity, err)
		return
	}
	if err := s.apply(reqOpts(r)); err != nil {
		s.rollbackFragment(dir, name, old, existed)
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "session": name})
}

func (s *Server) patchSession(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if !validSessionName(name) {
		writeErr(w, http.StatusBadRequest, fmt.Errorf("invalid session name %q", name))
		return
	}
	var patch map[string]any
	if err := json.NewDecoder(r.Body).Decode(&patch); err != nil || len(patch) == 0 {
		writeErr(w, http.StatusBadRequest, fmt.Errorf("invalid JSON body: %v", err))
		return
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	dir := s.sessionsDir()
	old, fields, err := s.readFragment(dir, name)
	if err != nil {
		writeErr(w, http.StatusNotFound, fmt.Errorf("session %q is not API-managed", name))
		return
	}
	if fields == nil {
		fields = map[string]any{}
	}
	for k, v := range patch {
		if v == nil {
			delete(fields, k)
		} else {
			fields[k] = v
		}
	}
	if err := s.writeFragment(dir, name, fields); err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	if _, err := s.load(); err != nil {
		s.rollbackFragment(dir, name, old, true)
		writeErr(w, http.StatusUnprocessableEntity, err)
		return
	}
	if err := s.apply(reqOpts(r)); err != nil {
		s.rollbackFragment(dir, name, old, true)
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "session": name})
}

func (s *Server) deleteSession(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if !validSessionName(name) {
		writeErr(w, http.StatusBadRequest, fmt.Errorf("invalid session name %q", name))
		return
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	dir := s.sessionsDir()
	old, err := os.ReadFile(s.fragmentPath(dir, name))
	if err != nil {
		writeErr(w, http.StatusNotFound, fmt.Errorf("session %q is not API-managed", name))
		return
	}
	if err := os.Remove(s.fragmentPath(dir, name)); err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	if err := s.apply(reqOpts(r)); err != nil {
		s.rollbackFragment(dir, name, old, true)
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "session": name})
}

func (s *Server) setSessionDisabled(disabled bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		name := r.PathValue("name")
		s.mu.Lock()
		defer s.mu.Unlock()

		dir := s.sessionsDir()
		old, fields, err := s.readFragment(dir, name)
		if err != nil {
			writeErr(w, http.StatusNotFound, fmt.Errorf("session %q is not API-managed", name))
			return
		}
		if fields == nil {
			fields = map[string]any{}
		}
		fields["disabled"] = disabled
		if err := s.writeFragment(dir, name, fields); err != nil {
			writeErr(w, http.StatusInternalServerError, err)
			return
		}
		if err := s.apply(reqOpts(r)); err != nil {
			s.rollbackFragment(dir, name, old, true)
			writeErr(w, http.StatusInternalServerError, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "session": name, "disabled": disabled})
	}
}

// rollbackFragment restores or removes a session fragment after a failed apply
func (s *Server) rollbackFragment(dir, name string, old []byte, existed bool) {
	if existed {
		if err := os.WriteFile(s.fragmentPath(dir, name), old, 0644); err != nil {
			log.Errorf("api: restoring session fragment %s: %s", name, err)
		}
	} else {
		if err := os.Remove(s.fragmentPath(dir, name)); err != nil && !os.IsNotExist(err) {
			log.Errorf("api: removing session fragment %s: %s", name, err)
		}
	}
}

func (s *Server) getConfig(w http.ResponseWriter, r *http.Request) {
	b, err := os.ReadFile(s.ConfigFile)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	sanitized := sensitiveRegex.ReplaceAll(b, []byte("${1}${2}: REDACTED"))
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	_, _ = w.Write(sanitized)
}

func (s *Server) putConfig(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	// Stage and validate before touching the live config
	staged := s.ConfigFile + ".staged"
	if err := os.WriteFile(staged, body, 0644); err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	if _, err := process.LoadFile(staged); err != nil {
		os.Remove(staged)
		writeErr(w, http.StatusUnprocessableEntity, err)
		return
	}

	old, _ := os.ReadFile(s.ConfigFile)
	if err := os.Rename(staged, s.ConfigFile); err != nil {
		os.Remove(staged)
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	if err := s.apply(reqOpts(r)); err != nil {
		if len(old) > 0 {
			if werr := os.WriteFile(s.ConfigFile, old, 0644); werr != nil {
				log.Errorf("api: restoring config file: %s", werr)
			}
		}
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) generate(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.apply(reqOpts(r)); err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) birdCommand(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Command string `json:"command"`
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	if !strings.HasPrefix(strings.TrimSpace(in.Command), "show ") {
		writeErr(w, http.StatusForbidden, fmt.Errorf("only 'show' commands are allowed"))
		return
	}
	c, err := s.load()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	resp, _, err := bird.RunCommand(in.Command, c.BIRDSocket, birdTimeout(c))
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"output": resp})
}

func (s *Server) reportNow(w http.ResponseWriter, r *http.Request) {
	c, err := s.load()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	router := c.ReportRouter
	if router == "" {
		router = c.Hostname
	}
	rep, err := report.Collect(c, router, c.ReportPrefixCap)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	if r.URL.Query().Get("post") == "1" {
		if c.ReportURL == "" {
			writeErr(w, http.StatusBadRequest, fmt.Errorf("report-url is not configured"))
			return
		}
		if err := report.Post(rep, c.ReportURL, c.ReportKey); err != nil {
			writeErr(w, http.StatusBadGateway, err)
			return
		}
	}
	writeJSON(w, http.StatusOK, rep)
}

// ---- prefix rules ----

// rulesPath is the API-managed prefix-rules fragment. The leading underscore
// keeps it out of session reconciliation.
func (s *Server) rulesPath(dir string) string {
	return filepath.Join(dir, "_prefix-rules.yml")
}

func (s *Server) writeRulesFragment(dir string, rules []map[string]any) error {
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}
	b, err := yaml.Marshal(map[string]any{"prefix-rules": rules})
	if err != nil {
		return err
	}
	header := []byte("# Managed by pathvector API - do not edit\n")
	return os.WriteFile(s.rulesPath(dir), append(header, b...), 0644)
}

func (s *Server) getRules(w http.ResponseWriter, r *http.Request) {
	b, err := os.ReadFile(s.rulesPath(s.sessionsDir()))
	if os.IsNotExist(err) {
		writeJSON(w, http.StatusOK, map[string]any{"rules": []any{}})
		return
	}
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	var doc struct {
		Rules []map[string]any `yaml:"prefix-rules"`
	}
	if err := yaml.Unmarshal(b, &doc); err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"rules": doc.Rules})
}

// putRules replaces the API-managed prefix-rules set. Body: {"rules": [...]}.
// An empty set removes the fragment.
func (s *Server) putRules(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Rules []map[string]any `json:"rules"`
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeErr(w, http.StatusBadRequest, fmt.Errorf("invalid JSON body: %v", err))
		return
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	dir := s.sessionsDir()
	old, readErr := os.ReadFile(s.rulesPath(dir))
	existed := readErr == nil
	rollback := func() {
		if existed {
			if err := os.WriteFile(s.rulesPath(dir), old, 0644); err != nil {
				log.Errorf("api: restoring rules fragment: %s", err)
			}
		} else {
			_ = os.Remove(s.rulesPath(dir))
		}
	}

	if len(in.Rules) == 0 {
		_ = os.Remove(s.rulesPath(dir))
	} else if err := s.writeRulesFragment(dir, in.Rules); err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	if _, err := s.load(); err != nil {
		rollback()
		writeErr(w, http.StatusUnprocessableEntity, err)
		return
	}
	if err := s.apply(reqOpts(r)); err != nil {
		rollback()
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "rules": len(in.Rules)})
}

func (s *Server) deleteRules(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()

	dir := s.sessionsDir()
	old, err := os.ReadFile(s.rulesPath(dir))
	if err != nil {
		writeErr(w, http.StatusNotFound, fmt.Errorf("no API-managed prefix rules"))
		return
	}
	if err := os.Remove(s.rulesPath(dir)); err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	if err := s.apply(reqOpts(r)); err != nil {
		if werr := os.WriteFile(s.rulesPath(dir), old, 0644); werr != nil {
			log.Errorf("api: restoring rules fragment: %s", werr)
		}
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// ---- reconcile ----

// reconcileRequest is the desired-state document for POST /v1/reconcile
type reconcileRequest struct {
	Sessions map[string]map[string]any `json:"sessions"`        // desired API-managed sessions
	Rules    *[]map[string]any         `json:"rules"`           // if non-null, replaces the prefix-rules set
	Globals  map[string]any            `json:"globals"`         // merged into the base config (whitelisted keys only)
	Prune    bool                      `json:"prune,omitempty"` // delete API-managed sessions not in sessions
}

// globalsAllowlist gates which base-config keys reconcile may set —
// everything else (asn, api-*, templates, credentials) stays operator-managed.
var globalsAllowlist = map[string]bool{
	"prefixes":           true,
	"origin-communities": true,
	"local-communities":  true,
}

// fragmentEquals reports whether the existing fragment file already encodes
// the desired peer fields (compared through canonical YAML marshalling)
func fragmentEquals(existing []byte, name string, fields map[string]any) bool {
	var doc map[string]any
	if err := yaml.Unmarshal(existing, &doc); err != nil {
		return false
	}
	peers, _ := doc["peers"].(map[string]any)
	current, _ := peers[name].(map[string]any)
	a, err1 := yaml.Marshal(current)
	b, err2 := yaml.Marshal(fields)
	return err1 == nil && err2 == nil && string(a) == string(b)
}

// reconcile applies a desired-state document in a single load/validate/apply
// cycle instead of one cycle per session mutation
func (s *Server) reconcile(w http.ResponseWriter, r *http.Request) {
	var in reconcileRequest
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeErr(w, http.StatusBadRequest, fmt.Errorf("invalid JSON body: %v", err))
		return
	}
	for name := range in.Sessions {
		if !validSessionName(name) {
			writeErr(w, http.StatusBadRequest, fmt.Errorf("invalid session name %q", name))
			return
		}
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	// Snapshot the sessions directory AND the base config for rollback
	dir := s.sessionsDir()
	snapshot := map[string][]byte{}
	frags, _ := filepath.Glob(filepath.Join(dir, "*.yml"))
	for _, f := range frags {
		if b, err := os.ReadFile(f); err == nil {
			snapshot[f] = b
		}
	}
	baseSnapshot, _ := os.ReadFile(s.ConfigFile)
	restore := func() {
		for f, b := range snapshot {
			if err := os.WriteFile(f, b, 0644); err != nil {
				log.Errorf("api: restoring fragment %s: %s", f, err)
			}
		}
		current, _ := filepath.Glob(filepath.Join(dir, "*.yml"))
		for _, f := range current {
			if _, ok := snapshot[f]; !ok {
				_ = os.Remove(f)
			}
		}
		if baseSnapshot != nil {
			if err := os.WriteFile(s.ConfigFile, baseSnapshot, 0644); err != nil {
				log.Errorf("api: restoring base config: %s", err)
			}
		}
	}

	// Merge whitelisted global keys into the base config before validating —
	// the panel pushes originated prefixes + origin-community tags here so
	// export scoping stays in sync with IPAM without a manual edit.
	if len(in.Globals) > 0 {
		doc := map[string]any{}
		if err := yaml.Unmarshal(baseSnapshot, &doc); err != nil {
			writeErr(w, http.StatusUnprocessableEntity, fmt.Errorf("base config does not parse: %v", err))
			return
		}
		for k := range in.Globals {
			if !globalsAllowlist[k] {
				writeErr(w, http.StatusForbidden, fmt.Errorf("globals key %q is not allowlisted", k))
				return
			}
		}
		for k, v := range in.Globals {
			doc[k] = v
		}
		if b, err := yaml.Marshal(doc); err == nil {
			if err := os.WriteFile(s.ConfigFile, b, 0644); err != nil {
				writeErr(w, http.StatusInternalServerError, err)
				return
			}
		}
	}

	var created, updated, deleted, unchanged []string

	if err := os.MkdirAll(dir, 0755); err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	for name, fields := range in.Sessions {
		p := s.fragmentPath(dir, name)
		if old, ok := snapshot[p]; ok && fragmentEquals(old, name, fields) {
			unchanged = append(unchanged, name)
			continue
		}
		if err := s.writeFragment(dir, name, fields); err != nil {
			restore()
			writeErr(w, http.StatusInternalServerError, err)
			return
		}
		if _, existed := snapshot[p]; existed {
			updated = append(updated, name)
		} else {
			created = append(created, name)
		}
	}

	if in.Prune {
		for f := range snapshot {
			base := filepath.Base(f)
			if strings.HasPrefix(base, "_") {
				continue // non-session fragments are managed separately
			}
			name := strings.TrimSuffix(base, ".yml")
			if _, ok := in.Sessions[name]; !ok {
				if err := os.Remove(f); err != nil {
					restore()
					writeErr(w, http.StatusInternalServerError, err)
					return
				}
				deleted = append(deleted, name)
			}
		}
	}

	if in.Rules != nil {
		if len(*in.Rules) == 0 {
			_ = os.Remove(s.rulesPath(dir))
		} else if err := s.writeRulesFragment(dir, *in.Rules); err != nil {
			restore()
			writeErr(w, http.StatusInternalServerError, err)
			return
		}
	}

	if _, err := s.load(); err != nil {
		restore()
		writeErr(w, http.StatusUnprocessableEntity, err)
		return
	}
	if err := s.apply(reqOpts(r)); err != nil {
		restore()
		writeErr(w, http.StatusInternalServerError, err)
		return
	}

	sort.Strings(created)
	sort.Strings(updated)
	sort.Strings(deleted)
	sort.Strings(unchanged)
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":        true,
		"created":   created,
		"updated":   updated,
		"deleted":   deleted,
		"unchanged": unchanged,
	})
}
