package handlers

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/openagentsinc/bahia/internal/api/middleware"
	"github.com/openagentsinc/bahia/internal/auth"
	"github.com/openagentsinc/bahia/internal/config"
	"github.com/openagentsinc/bahia/internal/service"
)

const ociAPIVersionHeader = "registry/2.0"

type ociErrorEnvelope struct {
	Errors []ociError `json:"errors"`
}

type ociError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	Detail  any    `json:"detail,omitempty"`
}

// OCIRegistryHandler serves OCI distribution pull routes.
type OCIRegistryHandler struct {
	svc    *service.OCIRegistryService
	nip98  *auth.NIP98Validator
	ociCfg config.OCIServerConfig
}

func NewOCIRegistryHandler(svc *service.OCIRegistryService, nip98 *auth.NIP98Validator, ociCfg config.OCIServerConfig) *OCIRegistryHandler {
	return &OCIRegistryHandler{svc: svc, nip98: nip98, ociCfg: ociCfg}
}

func (h *OCIRegistryHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Docker-Distribution-API-Version", ociAPIVersionHeader)
	if h.svc == nil {
		writeOCIError(w, http.StatusServiceUnavailable, "UNAVAILABLE", "registry service unavailable", nil)
		return
	}

	r = middleware.WithRegistryPrincipal(r, middleware.ResolveRegistryPrincipal(r, h.nip98, h.ociCfg))

	path := r.URL.Path
	if strings.HasPrefix(path, "/v2") {
		path = strings.TrimPrefix(path, "/v2")
	}
	if path == "" {
		path = "/"
	}

	if path == "/" {
		h.handleAPIBase(w, r)
		return
	}

	if repo, value, ok := splitRepoAndValue(path, "/blobs/uploads/"); ok {
		suffix := strings.TrimPrefix(value, "/")
		h.handleBlobUploadRoute(w, r, repo, suffix)
		return
	}
	if repo, ok := splitRepoOnly(path, "/blobs/uploads/"); ok {
		h.handleBlobUploadRoute(w, r, repo, "")
		return
	}
	if repo, ref, ok := splitRepoAndValue(path, "/manifests/"); ok {
		h.handleManifest(w, r, repo, ref)
		return
	}
	if repo, dgst, ok := splitRepoAndValue(path, "/blobs/"); ok {
		h.handleBlob(w, r, repo, dgst)
		return
	}
	if repo, ok := splitRepoOnly(path, "/tags/list"); ok {
		h.handleTagsList(w, r, repo)
		return
	}
	if repo, dgst, ok := splitRepoAndValue(path, "/referrers/"); ok {
		h.handleReferrers(w, r, repo, dgst)
		return
	}

	writeOCIError(w, http.StatusNotFound, "NAME_UNKNOWN", "repository or route not found", path)
}

func (h *OCIRegistryHandler) handleAPIBase(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	_, _ = h.svc.CheckAPI(r.Context())
	w.WriteHeader(http.StatusOK)
}

func (h *OCIRegistryHandler) handleManifest(w http.ResponseWriter, r *http.Request, repo, reference string) {
	if r.Method == http.MethodPut {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			writeOCIError(w, http.StatusBadRequest, "MANIFEST_INVALID", "unable to read manifest", nil)
			return
		}
		sum := sha256.Sum256(body)
		digest := "sha256:" + hex.EncodeToString(sum[:])
		storedDigest, err := h.svc.PutManifest(r.Context(), repo, reference, r.Header.Get("Content-Type"), digest, body)
		if err != nil {
			writeOCIServiceError(w, err)
			return
		}
		w.Header().Set("Docker-Content-Digest", storedDigest)
		w.Header().Set("Location", "/v2/"+repo+"/manifests/"+reference)
		w.WriteHeader(http.StatusCreated)
		return
	}
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	resp, err := h.svc.FetchManifest(r.Context(), repo, reference, r.Header.Get("Accept"))
	if err != nil {
		writeOCIServiceError(w, err)
		return
	}
	w.Header().Set("Content-Type", resp.ContentType)
	w.Header().Set("Docker-Content-Digest", resp.DockerContentDigest)
	w.Header().Set("Content-Length", strconv.FormatInt(resp.ContentLength, 10))
	if r.Method == http.MethodHead {
		w.WriteHeader(http.StatusOK)
		return
	}
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(resp.Content)
}

func (h *OCIRegistryHandler) handleBlob(w http.ResponseWriter, r *http.Request, repo, digest string) {
	if r.Method == http.MethodHead {
		resp, err := h.svc.ProxyBlobHEAD(r.Context(), repo, digest)
		if err != nil {
			writeOCIServiceError(w, err)
			return
		}
		if !resp.Exists {
			writeOCIError(w, http.StatusNotFound, "BLOB_UNKNOWN", "blob not found", digest)
			return
		}
		w.Header().Set("Content-Type", resp.ContentType)
		w.Header().Set("Docker-Content-Digest", resp.DockerContentDigest)
		w.Header().Set("Content-Length", strconv.FormatInt(resp.ContentLength, 10))
		for k, v := range resp.Header {
			if w.Header().Get(k) == "" {
				w.Header().Set(k, v)
			}
		}
		w.WriteHeader(http.StatusOK)
		return
	}
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	resp, err := h.svc.ProxyBlobGET(r.Context(), repo, digest)
	if err != nil {
		writeOCIServiceError(w, err)
		return
	}
	defer resp.Stream.Close()
	w.Header().Set("Content-Type", resp.ContentType)
	w.Header().Set("Docker-Content-Digest", resp.DockerContentDigest)
	w.Header().Set("Content-Length", strconv.FormatInt(resp.ContentLength, 10))
	w.WriteHeader(http.StatusOK)
	_, _ = io.Copy(w, resp.Stream.Body)
}

func (h *OCIRegistryHandler) handleTagsList(w http.ResponseWriter, r *http.Request, repo string) {
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	n := queryInt(r, "n", 0)
	last := r.URL.Query().Get("last")
	resp, err := h.svc.ListTags(r.Context(), repo, n, last)
	if err != nil {
		writeOCIServiceError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

func (h *OCIRegistryHandler) handleBlobUploadRoute(w http.ResponseWriter, r *http.Request, repo, uploadID string) {
	if err := h.svc.AuthorizePush(r.Context(), repo); err != nil {
		writeOCIServiceError(w, err)
		return
	}
	switch r.Method {
	case http.MethodPost:
		if uploadID != "" {
			writeOCIError(w, http.StatusNotFound, "BLOB_UPLOAD_UNKNOWN", "upload session unknown", uploadID)
			return
		}
		h.handleBlobUploadStart(w, r, repo)
	case http.MethodPatch:
		if uploadID == "" {
			writeOCIError(w, http.StatusNotFound, "BLOB_UPLOAD_UNKNOWN", "upload session unknown", nil)
			return
		}
		h.handleBlobUploadPatch(w, r, repo, uploadID)
	case http.MethodPut:
		if uploadID == "" {
			writeOCIError(w, http.StatusNotFound, "BLOB_UPLOAD_UNKNOWN", "upload session unknown", nil)
			return
		}
		h.handleBlobUploadFinalize(w, r, repo, uploadID)
	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

func (h *OCIRegistryHandler) handleBlobUploadStart(w http.ResponseWriter, r *http.Request, repo string) {
	digest := strings.TrimSpace(r.URL.Query().Get("digest"))
	mountDigest := strings.TrimSpace(r.URL.Query().Get("mount"))
	fromRepo := strings.TrimSpace(r.URL.Query().Get("from"))
	if mountDigest != "" && fromRepo != "" {
		blob, mounted, err := h.svc.MountBlob(r.Context(), repo, fromRepo, mountDigest)
		if err != nil {
			writeOCIServiceError(w, err)
			return
		}
		if mounted {
			w.Header().Set("Location", "/v2/"+repo+"/blobs/"+blob.Digest)
			w.Header().Set("Docker-Content-Digest", blob.Digest)
			w.WriteHeader(http.StatusCreated)
			return
		}
	}

	upload, err := h.svc.BeginUpload(r.Context(), repo)
	if err != nil {
		writeOCIServiceError(w, err)
		return
	}
	location := "/v2/" + repo + "/blobs/uploads/" + upload.UploadID

	if digest != "" {
		blob, err := h.svc.FinalizeUpload(r.Context(), upload.UploadID, r.Body, r.ContentLength, digest)
		if err != nil {
			writeOCIServiceError(w, err)
			return
		}
		w.Header().Set("Location", "/v2/"+repo+"/blobs/"+blob.Digest)
		w.Header().Set("Docker-Content-Digest", blob.Digest)
		w.WriteHeader(http.StatusCreated)
		return
	}

	writeUploadHeaders(w, location, upload.UploadID, upload.OffsetBytes)
	w.WriteHeader(http.StatusAccepted)
}

func (h *OCIRegistryHandler) handleBlobUploadPatch(w http.ResponseWriter, r *http.Request, repo, uploadID string) {
	start, err := parseUploadRangeStart(r.Header.Get("Content-Range"))
	if err != nil {
		writeOCIError(w, http.StatusRequestedRangeNotSatisfiable, "RANGE_INVALID", "invalid Content-Range", nil)
		return
	}
	upload, err := h.svc.AppendUpload(r.Context(), uploadID, r.Body, r.ContentLength, start)
	if err != nil {
		writeOCIServiceError(w, err)
		return
	}
	location := "/v2/" + repo + "/blobs/uploads/" + uploadID
	writeUploadHeaders(w, location, uploadID, upload.OffsetBytes)
	w.WriteHeader(http.StatusAccepted)
}

func (h *OCIRegistryHandler) handleBlobUploadFinalize(w http.ResponseWriter, r *http.Request, repo, uploadID string) {
	digest := strings.TrimSpace(r.URL.Query().Get("digest"))
	if digest == "" {
		writeOCIError(w, http.StatusBadRequest, "DIGEST_INVALID", "digest query parameter is required", nil)
		return
	}
	blob, err := h.svc.FinalizeUpload(r.Context(), uploadID, r.Body, r.ContentLength, digest)
	if err != nil {
		writeOCIServiceError(w, err)
		return
	}
	w.Header().Set("Location", "/v2/"+repo+"/blobs/"+blob.Digest)
	w.Header().Set("Docker-Content-Digest", blob.Digest)
	w.WriteHeader(http.StatusCreated)
}

func (h *OCIRegistryHandler) handleReferrers(w http.ResponseWriter, r *http.Request, repo, digest string) {
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	artifactType := r.URL.Query().Get("artifactType")
	resp, err := h.svc.ListReferrers(r.Context(), repo, digest, artifactType)
	if err != nil {
		writeOCIServiceError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

func splitRepoAndValue(path, marker string) (string, string, bool) {
	idx := strings.LastIndex(path, marker)
	if idx <= 0 {
		return "", "", false
	}
	repo := strings.TrimPrefix(path[:idx], "/")
	value := strings.TrimPrefix(path[idx+len(marker):], "/")
	if repo == "" || value == "" {
		return "", "", false
	}
	return repo, value, true
}

func splitRepoOnly(path, suffix string) (string, bool) {
	if !strings.HasSuffix(path, suffix) {
		return "", false
	}
	repo := strings.TrimPrefix(strings.TrimSuffix(path, suffix), "/")
	if repo == "" {
		return "", false
	}
	return repo, true
}

func writeOCIServiceError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, service.ErrRegistryUnauthorized):
		writeOCIError(w, http.StatusUnauthorized, "UNAUTHORIZED", "authentication required", nil)
	case errors.Is(err, service.ErrManifestNotFound):
		writeOCIError(w, http.StatusNotFound, "MANIFEST_UNKNOWN", "manifest unknown", nil)
	case errors.Is(err, service.ErrBlobNotFound):
		writeOCIError(w, http.StatusNotFound, "BLOB_UNKNOWN", "blob unknown", nil)
	case errors.Is(err, service.ErrManifestNotAcceptable):
		writeOCIError(w, http.StatusNotAcceptable, "MANIFEST_INVALID", "manifest media type not acceptable", nil)
	case errors.Is(err, service.ErrRegistryPushUnauthorized):
		writeOCIError(w, http.StatusUnauthorized, "UNAUTHORIZED", "push authorization required", nil)
	case errors.Is(err, service.ErrUploadNotFound):
		writeOCIError(w, http.StatusNotFound, "BLOB_UPLOAD_UNKNOWN", "upload session unknown", nil)
	case errors.Is(err, service.ErrUploadDigestMismatch):
		writeOCIError(w, http.StatusBadRequest, "DIGEST_INVALID", "provided digest did not match uploaded content", nil)
	case errors.Is(err, service.ErrUploadOffsetMismatch):
		writeOCIError(w, http.StatusRequestedRangeNotSatisfiable, "RANGE_INVALID", "upload offset did not match", nil)
	case errors.Is(err, service.ErrUploadLengthRequired):
		writeOCIError(w, http.StatusLengthRequired, "SIZE_INVALID", "content-length is required", nil)
	case errors.Is(err, service.ErrManifestInvalid):
		writeOCIError(w, http.StatusBadRequest, "MANIFEST_INVALID", "manifest invalid", nil)
	case errors.Is(err, service.ErrManifestDigestMismatch):
		writeOCIError(w, http.StatusBadRequest, "DIGEST_INVALID", "manifest digest mismatch", nil)
	case errors.Is(err, service.ErrBlobUnknown):
		writeOCIError(w, http.StatusBadRequest, "BLOB_UNKNOWN", "referenced blob unknown", nil)
	default:
		writeOCIError(w, http.StatusInternalServerError, "UNKNOWN", "internal error", err.Error())
	}
}

func writeUploadHeaders(w http.ResponseWriter, location, uploadID string, offset int64) {
	w.Header().Set("Location", location)
	w.Header().Set("Docker-Upload-UUID", uploadID)
	if offset <= 0 {
		w.Header().Set("Range", "0-0")
		return
	}
	w.Header().Set("Range", "0-"+strconv.FormatInt(offset-1, 10))
}

func parseUploadRangeStart(contentRange string) (*int64, error) {
	contentRange = strings.TrimSpace(contentRange)
	if contentRange == "" {
		return nil, nil
	}
	if strings.HasPrefix(strings.ToLower(contentRange), "bytes ") {
		contentRange = strings.TrimSpace(contentRange[6:])
	}
	return service.ParseUploadOffset(contentRange)
}

func writeOCIError(w http.ResponseWriter, status int, code, message string, detail any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(ociErrorEnvelope{Errors: []ociError{{Code: code, Message: message, Detail: detail}}})
}
