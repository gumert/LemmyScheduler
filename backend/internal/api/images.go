package api

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gumert/LemmyScheduler/backend/internal/scheduledposts"
)

type ImageServer struct {
	store   scheduledposts.ImageStore
	maxSize int64
}

func NewImageServer(store scheduledposts.ImageStore) *ImageServer {
	return &ImageServer{store: store, maxSize: scheduledposts.MaxImageBytes}
}

// Handler serves the image routes. Method patterns let the mux answer 405 with
// an Allow header itself. The caller must wrap it in CORS: preflight OPTIONS
// requests are answered there, before the mux's method matching can reject them.
func (s *ImageServer) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("PUT /scheduled-posts/{id}/title-image", s.putTitle)
	mux.HandleFunc("GET /scheduled-posts/{id}/title-image", s.getTitle)
	mux.HandleFunc("DELETE /scheduled-posts/{id}/title-image", s.deleteTitle)
	mux.HandleFunc("PUT /scheduled-posts/{id}/title-image/{image_id}", s.selectTitle)
	mux.HandleFunc("POST /scheduled-posts/{id}/images", s.createBody)
	mux.HandleFunc("GET /scheduled-posts/{id}/images/{image_id}", s.getBody)
	mux.HandleFunc("DELETE /scheduled-posts/{id}/images/{image_id}", s.deleteBody)
	return mux
}

func (s *ImageServer) putTitle(w http.ResponseWriter, r *http.Request) {
	if postID, ok := imageID(w, r.PathValue("id")); ok {
		s.upload(w, r, postID, true)
	}
}

func (s *ImageServer) getTitle(w http.ResponseWriter, r *http.Request) {
	if postID, ok := imageID(w, r.PathValue("id")); ok {
		s.get(w, r, postID, 0, true)
	}
}

func (s *ImageServer) deleteTitle(w http.ResponseWriter, r *http.Request) {
	if postID, ok := imageID(w, r.PathValue("id")); ok {
		s.delete(w, r, postID, 0, true)
	}
}

func (s *ImageServer) createBody(w http.ResponseWriter, r *http.Request) {
	if postID, ok := imageID(w, r.PathValue("id")); ok {
		s.upload(w, r, postID, false)
	}
}

func (s *ImageServer) selectTitle(w http.ResponseWriter, r *http.Request) {
	postID, id, ok := imageIDs(w, r)
	if !ok {
		return
	}
	if err := s.store.SelectTitleImage(r.Context(), postID, id); err != nil {
		imageError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *ImageServer) getBody(w http.ResponseWriter, r *http.Request) {
	if postID, id, ok := imageIDs(w, r); ok {
		s.get(w, r, postID, id, false)
	}
}

func (s *ImageServer) deleteBody(w http.ResponseWriter, r *http.Request) {
	if postID, id, ok := imageIDs(w, r); ok {
		s.delete(w, r, postID, id, false)
	}
}

func imageIDs(w http.ResponseWriter, r *http.Request) (postID, id int64, ok bool) {
	if postID, ok = imageID(w, r.PathValue("id")); !ok {
		return 0, 0, false
	}
	if id, ok = imageID(w, r.PathValue("image_id")); !ok {
		return 0, 0, false
	}
	return postID, id, true
}

func imageID(w http.ResponseWriter, value string) (int64, bool) {
	id, err := strconv.ParseInt(value, 10, 64)
	if err != nil || id <= 0 {
		http.Error(w, "id must be a positive integer", http.StatusBadRequest)
		return 0, false
	}
	return id, true
}

func (s *ImageServer) upload(w http.ResponseWriter, r *http.Request, postID int64, title bool) {
	controller := http.NewResponseController(w)
	deadline := time.Now().Add(time.Minute)
	for _, set := range []func(time.Time) error{controller.SetReadDeadline, controller.SetWriteDeadline} {
		if err := set(deadline); err != nil && !errors.Is(err, http.ErrNotSupported) {
			log.Printf("image upload deadline: %v", err)
			http.Error(w, "could not set upload deadline", http.StatusInternalServerError)
			return
		}
	}
	data, err := io.ReadAll(http.MaxBytesReader(w, r.Body, s.maxSize))
	if err != nil {
		var maxErr *http.MaxBytesError
		if errors.As(err, &maxErr) {
			http.Error(w, "image is too large", http.StatusRequestEntityTooLarge)
		} else {
			http.Error(w, "could not read image", http.StatusBadRequest)
		}
		return
	}
	if len(data) == 0 {
		http.Error(w, "image is empty", http.StatusBadRequest)
		return
	}
	contentType := http.DetectContentType(data)
	switch contentType {
	case "image/jpeg", "image/png", "image/gif", "image/webp":
	default:
		http.Error(w, "unsupported image type", http.StatusUnsupportedMediaType)
		return
	}
	var image scheduledposts.ImageMetadata
	if title {
		image, err = s.store.PutTitleImage(r.Context(), postID, contentType, data)
	} else {
		image, err = s.store.CreateBodyImage(r.Context(), postID, contentType, data)
	}
	if err != nil {
		imageError(w, err)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	if !title {
		w.WriteHeader(http.StatusCreated)
	}
	response := struct {
		ID          string    `json:"id"`
		ContentType string    `json:"content_type"`
		CreatedAt   time.Time `json:"created_at"`
	}{strconv.FormatInt(image.ID, 10), image.ContentType, image.CreatedAt}
	if err := json.NewEncoder(w).Encode(response); err != nil {
		log.Printf("write image upload response: %v", err)
	}
}

func (s *ImageServer) get(w http.ResponseWriter, r *http.Request, postID, id int64, title bool) {
	image, err := s.store.GetImage(r.Context(), postID, id, title)
	if err != nil {
		imageError(w, err)
		return
	}
	etag := fmt.Sprintf("\"%x\"", sha256.Sum256(image.Data))
	w.Header().Set("Content-Type", image.ContentType)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Cache-Control", "private, max-age=0, must-revalidate")
	w.Header().Set("ETag", etag)
	for _, value := range strings.Split(r.Header.Get("If-None-Match"), ",") {
		value = strings.TrimSpace(value)
		if value == "*" || strings.TrimPrefix(value, "W/") == etag {
			w.WriteHeader(http.StatusNotModified)
			return
		}
	}
	if _, err := w.Write(image.Data); err != nil {
		log.Printf("write image response: %v", err)
	}
}

func (s *ImageServer) delete(w http.ResponseWriter, r *http.Request, postID, id int64, title bool) {
	if err := s.store.DeleteImage(r.Context(), postID, id, title); err != nil {
		imageError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func imageError(w http.ResponseWriter, err error) {
	var validationErr *scheduledposts.ValidationError
	switch {
	case errors.As(err, &validationErr):
		http.Error(w, err.Error(), http.StatusBadRequest)
	case errors.Is(err, scheduledposts.ErrNotFound), errors.Is(err, scheduledposts.ErrImageNotFound):
		http.Error(w, err.Error(), http.StatusNotFound)
	case errors.Is(err, scheduledposts.ErrNotEditable), errors.Is(err, scheduledposts.ErrImageInUse):
		http.Error(w, err.Error(), http.StatusConflict)
	case errors.Is(err, scheduledposts.ErrImageLimit):
		http.Error(w, err.Error(), http.StatusRequestEntityTooLarge)
	default:
		log.Printf("image request: %v", err)
		http.Error(w, "image request failed", http.StatusInternalServerError)
	}
}
