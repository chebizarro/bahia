package handlers

import (
	"errors"
	"net/http"
	"os"

	"github.com/go-chi/chi/v5"
	userdocs "github.com/openagentsinc/bahia/internal/docs"
)

type DocsHandler struct {
	docs userdocs.Service
}

type DocsCatalogResponse struct {
	Topics []userdocs.Topic   `json:"topics"`
	Groups []DocsCatalogGroup `json:"groups"`
	Count  int                `json:"count"`
}

type DocsCatalogGroup struct {
	Category string           `json:"category"`
	Label    string           `json:"label"`
	Topics   []userdocs.Topic `json:"topics"`
}

type DocsDocumentResponse struct {
	Metadata userdocs.Topic          `json:"metadata"`
	Markdown string                  `json:"markdown"`
	Links    []userdocs.DocumentLink `json:"links"`
}

func NewDocsHandler(service userdocs.Service) *DocsHandler {
	return &DocsHandler{docs: service}
}

func (h *DocsHandler) Catalog(w http.ResponseWriter, r *http.Request) {
	if !h.requireDocsRoot(w) {
		return
	}
	catalog, err := h.docs.Catalog(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list documentation topics")
		return
	}
	writeData(w, http.StatusOK, DocsCatalogResponse{
		Topics: catalog,
		Groups: groupDocsCatalog(catalog),
		Count:  len(catalog),
	})
}

func (h *DocsHandler) Read(w http.ResponseWriter, r *http.Request) {
	if !h.requireDocsRoot(w) {
		return
	}
	topic := chi.URLParam(r, "topic")
	doc, err := h.docs.ReadWithLinks(r.Context(), topic)
	if err != nil {
		writeDocsError(w, topic, err)
		return
	}
	writeData(w, http.StatusOK, DocsDocumentResponse{
		Metadata: doc.Topic,
		Markdown: doc.Markdown,
		Links:    doc.Links,
	})
}

func (h *DocsHandler) requireDocsRoot(w http.ResponseWriter) bool {
	info, err := os.Stat(h.docs.BasePath())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "documentation root is unavailable")
		return false
	}
	if !info.IsDir() {
		writeError(w, http.StatusInternalServerError, "documentation root is not a directory")
		return false
	}
	return true
}

func writeDocsError(w http.ResponseWriter, topic string, err error) {
	switch {
	case errors.Is(err, userdocs.ErrInvalidTopic):
		writeError(w, http.StatusBadRequest, "invalid documentation topic: "+topic)
	case errors.Is(err, userdocs.ErrNotFound):
		writeError(w, http.StatusNotFound, "documentation topic not found: "+topic)
	default:
		writeError(w, http.StatusInternalServerError, "failed to read documentation topic: "+topic)
	}
}

func groupDocsCatalog(catalog []userdocs.Topic) []DocsCatalogGroup {
	groups := []DocsCatalogGroup{
		{Category: "guide", Label: "Getting Started & Guides"},
		{Category: "feature", Label: "Feature Guides"},
		{Category: "reference", Label: "Integration & Reference"},
	}
	byCategory := make(map[string][]userdocs.Topic, len(groups))
	for _, topic := range catalog {
		byCategory[topic.Category] = append(byCategory[topic.Category], topic)
	}
	for i := range groups {
		groups[i].Topics = byCategory[groups[i].Category]
	}
	return groups
}
