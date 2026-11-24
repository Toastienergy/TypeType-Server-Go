package api

import (
	"encoding/json"
	"io"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"typetype.dev/server/internal/httpx"
	"typetype.dev/server/internal/portability"
	"typetype.dev/server/internal/store"
)

var (
	portabilityOnce   sync.Once
	portabilityEngine *portability.Engine
	portabilityErr    error
)

// engine starts the portability engine on first use, its jobs under a fresh temporary directory like Kotlin's.
func (s *Server) engine() (*portability.Engine, error) {
	portabilityOnce.Do(func() {
		dir, err := os.MkdirTemp("", "typetype-portability-")
		if err != nil {
			portabilityErr = err
			return
		}
		st, err := portability.NewStore(dir)
		if err != nil {
			portabilityErr = err
			return
		}
		reg := portability.NewRegistry(func(raw []byte) ([]byte, error) {
			v := store.DefaultSettings()
			if err := json.Unmarshal(raw, &v); err != nil {
				return nil, portabilityInvalid("Invalid TypeType backup section")
			}
			v.Normalize()
			return json.Marshal(v)
		})
		portabilityEngine = &portability.Engine{Registry: reg, Store: st, Port: portabilityPort{s}, Log: s.Log}
	})
	return portabilityEngine, portabilityErr
}

func portabilityFail(w http.ResponseWriter, r *http.Request, err error) {
	e := portability.AsError(err)
	httpx.Error(w, r, e.Status, e.Message, e.Code)
}

// portabilityUser is withJwtAuth then withPortabilityAccount: guests have nothing to carry over.
func (s *Server) portabilityUser(next func(http.ResponseWriter, *http.Request, string, *portability.Engine)) http.HandlerFunc {
	return s.authed(func(w http.ResponseWriter, r *http.Request, user string) {
		if strings.HasPrefix(user, "guest:") {
			plain(w, r, http.StatusForbidden, "Guest users do not have portable account data")
			return
		}
		e, err := s.engine()
		if err != nil {
			portabilityFail(w, r, err)
			return
		}
		next(w, r, user, e)
	})
}

func requestIDOf(r *http.Request) *string {
	if id := httpx.RequestID(r.Context()); id != "" {
		return &id
	}
	return nil
}

// receiveBackup streams the single "file" part to a temporary file, refusing more than 512 MiB.
func receiveBackup(r *http.Request) (path, name, contentType string, err error) {
	mr, err := r.MultipartReader()
	if err != nil {
		return "", "", "", portabilityInvalid("Exactly one backup file is required")
	}
	files := 0
	for {
		p, err := mr.NextPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			return cleanup(path, portabilityInvalid("Exactly one backup file is required"))
		}
		if p.FormName() == "file" && p.FileName() != "" {
			files++
			if files == 1 {
				name, contentType = p.FileName(), p.Header.Get("Content-Type")
				if ct, _, err := mime.ParseMediaType(contentType); err == nil {
					contentType = ct
				}
				f, err := os.CreateTemp("", "portability-upload-*.tmp")
				if err != nil {
					return cleanup(path, err)
				}
				path = f.Name()
				n, err := io.Copy(f, io.LimitReader(p, portability.MaxUploadBytes+1))
				f.Close()
				if err != nil {
					return cleanup(path, err)
				}
				if n > portability.MaxUploadBytes {
					return cleanup(path, portability.ErrUploadTooBig)
				}
			}
		}
		p.Close()
	}
	if files != 1 {
		return cleanup(path, portabilityInvalid("Exactly one backup file is required"))
	}
	if name == "" {
		name = "backup"
	}
	return path, name, contentType, nil
}

func cleanup(path string, err error) (string, string, string, error) {
	if path != "" {
		os.Remove(path)
	}
	return "", "", "", err
}

func (s *Server) portabilityRoutes(rt *Router) {
	rt.Handle("GET", "/portability/formats", s.portabilityUser(func(w http.ResponseWriter, r *http.Request, _ string, e *portability.Engine) {
		httpx.JSON(w, http.StatusOK, e.Formats())
	}))
	rt.Handle("POST", "/portability/imports", s.portabilityUser(func(w http.ResponseWriter, r *http.Request, user string, e *portability.Engine) {
		hint, err := portability.ParseFormat(r.URL.Query().Get("format"), r.URL.Query().Has("format"))
		if err != nil {
			portabilityFail(w, r, err)
			return
		}
		path, name, ct, err := receiveBackup(r)
		if err != nil {
			portabilityFail(w, r, err)
			return
		}
		snap, err := e.StartImport(user, path, name, ct, hint, requestIDOf(r))
		os.Remove(path)
		if err != nil {
			portabilityFail(w, r, err)
			return
		}
		httpx.JSON(w, http.StatusAccepted, snap)
	}))
	rt.Handle("POST", "/portability/exports", s.portabilityUser(func(w http.ResponseWriter, r *http.Request, user string, e *portability.Engine) {
		var req portability.ExportRequest
		if json.NewDecoder(io.LimitReader(r.Body, 1<<16)).Decode(&req) != nil || !validFormat(req.Format) || !validCategories(req.Categories) {
			portabilityFail(w, r, portabilityInvalid("Invalid portability request"))
			return
		}
		snap, err := e.StartExport(user, req, requestIDOf(r))
		if err != nil {
			portabilityFail(w, r, err)
			return
		}
		httpx.JSON(w, http.StatusAccepted, snap)
	}))
	job := func(fn func(http.ResponseWriter, *http.Request, string, string, *portability.Engine)) http.HandlerFunc {
		return s.portabilityUser(func(w http.ResponseWriter, r *http.Request, user string, e *portability.Engine) {
			fn(w, r, user, r.PathValue("id"), e)
		})
	}
	rt.Handle("GET", "/portability/jobs/{id}", job(func(w http.ResponseWriter, r *http.Request, user, id string, e *portability.Engine) {
		answer[portability.Snapshot](w, r, http.StatusOK)(e.Snapshot(user, id))
	}))
	rt.Handle("GET", "/portability/jobs/{id}/report", job(func(w http.ResponseWriter, r *http.Request, user, id string, e *portability.Engine) {
		answer[portability.Report](w, r, http.StatusOK)(e.Report(user, id))
	}))
	rt.Handle("POST", "/portability/jobs/{id}/apply", job(func(w http.ResponseWriter, r *http.Request, user, id string, e *portability.Engine) {
		req := portability.ImportRequest{DuplicatePolicy: "skip"}
		if json.NewDecoder(io.LimitReader(r.Body, 1<<16)).Decode(&req) != nil || !validCategories(req.Categories) ||
			req.DuplicatePolicy != "skip" && req.DuplicatePolicy != "replace" {
			portabilityFail(w, r, portabilityInvalid("Invalid portability request"))
			return
		}
		answer[portability.Snapshot](w, r, http.StatusAccepted)(e.Apply(user, id, req))
	}))
	rt.Handle("POST", "/portability/jobs/{id}/cancel", job(func(w http.ResponseWriter, r *http.Request, user, id string, e *portability.Engine) {
		answer[portability.Snapshot](w, r, http.StatusOK)(e.Cancel(user, id))
	}))
	rt.Handle("GET", "/portability/jobs/{id}/artifact", job(func(w http.ResponseWriter, r *http.Request, user, id string, e *portability.Engine) {
		path, err := e.Artifact(user, id)
		if err != nil {
			portabilityFail(w, r, err)
			return
		}
		w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": filepath.Base(path)}))
		http.ServeFile(w, r, path)
	}))
	rt.Handle("DELETE", "/portability/jobs/{id}", job(func(w http.ResponseWriter, r *http.Request, user, id string, e *portability.Engine) {
		if err := e.Delete(user, id); err != nil {
			portabilityFail(w, r, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}))
}

func answer[T any](w http.ResponseWriter, r *http.Request, status int) func(T, error) {
	return func(v T, err error) {
		if err != nil {
			portabilityFail(w, r, err)
			return
		}
		httpx.JSON(w, status, v)
	}
}

func validFormat(f portability.Format) bool {
	_, err := portability.ParseFormat(string(f), true)
	return err == nil
}

func validCategories(cs []portability.Category) bool {
	for _, c := range cs {
		if !portability.ValidCategory(c) {
			return false
		}
	}
	return true
}
