package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gumert/LemmyScheduler/backend/internal/scheduledposts"
)

type fakeImageStore struct {
	title *scheduledposts.Image
	body  map[int64]scheduledposts.Image
	err   error
	next  int64
}

func (s *fakeImageStore) PutTitleImage(_ context.Context, _ int64, contentType string, data []byte) (scheduledposts.ImageMetadata, error) {
	if s.err != nil {
		return scheduledposts.ImageMetadata{}, s.err
	}
	s.next++
	s.title = &scheduledposts.Image{ImageMetadata: scheduledposts.ImageMetadata{ID: s.next, ContentType: contentType, CreatedAt: time.Now().UTC()}, Data: data}
	return s.title.ImageMetadata, nil
}

func (s *fakeImageStore) CreateBodyImage(_ context.Context, _ int64, contentType string, data []byte) (scheduledposts.ImageMetadata, error) {
	if s.err != nil {
		return scheduledposts.ImageMetadata{}, s.err
	}
	s.next++
	image := scheduledposts.Image{ImageMetadata: scheduledposts.ImageMetadata{ID: s.next, ContentType: contentType, CreatedAt: time.Now().UTC()}, Data: data}
	s.body[s.next] = image
	return image.ImageMetadata, nil
}

func (s *fakeImageStore) SelectTitleImage(_ context.Context, _, id int64) error {
	if s.err != nil {
		return s.err
	}
	image, ok := s.body[id]
	if !ok {
		return scheduledposts.ErrImageNotFound
	}
	s.title = &image
	return nil
}

func (s *fakeImageStore) GetImage(_ context.Context, _, id int64, title bool) (scheduledposts.Image, error) {
	if s.err != nil {
		return scheduledposts.Image{}, s.err
	}
	if title && s.title != nil {
		return *s.title, nil
	}
	if !title {
		if image, ok := s.body[id]; ok {
			return image, nil
		}
	}
	return scheduledposts.Image{}, scheduledposts.ErrImageNotFound
}

func (s *fakeImageStore) DeleteImage(_ context.Context, _, id int64, title bool) error {
	if s.err != nil {
		return s.err
	}
	if _, err := s.GetImage(context.Background(), 1, id, title); err != nil {
		return err
	}
	if title {
		s.title = nil
	} else {
		delete(s.body, id)
	}
	return nil
}

func imageTestMux(store *fakeImageStore) http.Handler {
	s := NewImageServer(store)
	s.maxSize = 64
	return s.Handler()
}

func imageRequest(h http.Handler, method, path string, body []byte, headers map[string]string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, bytes.NewReader(body))
	for key, value := range headers {
		req.Header.Set(key, value)
	}
	res := httptest.NewRecorder()
	h.ServeHTTP(res, req)
	return res
}

func TestImagesHTTP(t *testing.T) {
	store := &fakeImageStore{body: make(map[int64]scheduledposts.Image)}
	h := imageTestMux(store)
	png := []byte("\x89PNG\r\n\x1a\nimage")
	titlePath := "/scheduled-posts/1/title-image"
	bodyPath := "/scheduled-posts/1/images"
	if got := imageRequest(h, "PUT", titlePath, png, nil).Code; got != http.StatusOK {
		t.Fatalf("put title: %d", got)
	}
	replacement := []byte("\x89PNG\r\n\x1a\nreplacement")
	if got := imageRequest(h, "PUT", titlePath, replacement, nil).Code; got != http.StatusOK {
		t.Fatalf("replace title: %d", got)
	}
	if res := imageRequest(h, "GET", titlePath, nil, nil); res.Code != http.StatusOK || !bytes.Equal(res.Body.Bytes(), replacement) {
		t.Fatalf("get title = %d, %q", res.Code, res.Body)
	}
	res := imageRequest(h, "POST", bodyPath, png, nil)
	if res.Code != http.StatusCreated {
		t.Fatalf("create body: %d: %s", res.Code, res.Body)
	}
	var metadata struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(res.Body.Bytes(), &metadata); err != nil || metadata.ID != "3" {
		t.Fatalf("body image metadata = %s, error %v", res.Body, err)
	}
	if got := imageRequest(h, "PUT", titlePath+"/3", nil, nil).Code; got != http.StatusNoContent {
		t.Fatalf("select title: %d", got)
	}
	if res := imageRequest(h, "GET", titlePath, nil, nil); res.Code != http.StatusOK || !bytes.Equal(res.Body.Bytes(), png) {
		t.Fatalf("get selected title = %d, %q", res.Code, res.Body)
	}
	res = imageRequest(h, "GET", bodyPath+"/3", nil, nil)
	if res.Code != http.StatusOK || !bytes.Equal(res.Body.Bytes(), png) || res.Header().Get("Content-Type") != "image/png" || res.Header().Get("X-Content-Type-Options") != "nosniff" || res.Header().Get("Cache-Control") != "private, max-age=0, must-revalidate" {
		t.Fatalf("get image = %d, headers %v, body %q", res.Code, res.Header(), res.Body)
	}
	etag := res.Header().Get("ETag")
	if etag == "" {
		t.Fatal("missing ETag")
	}
	res = imageRequest(h, "GET", bodyPath+"/3", nil, map[string]string{"If-None-Match": `"other", W/` + etag})
	if res.Code != http.StatusNotModified || res.Body.Len() != 0 {
		t.Fatalf("conditional get = %d %q", res.Code, res.Body)
	}
	for _, path := range []string{titlePath, bodyPath + "/3"} {
		if got := imageRequest(h, "DELETE", path, nil, nil).Code; got != http.StatusNoContent {
			t.Fatalf("delete %s = %d", path, got)
		}
		if got := imageRequest(h, "GET", path, nil, nil).Code; got != http.StatusNotFound {
			t.Fatalf("get deleted %s = %d", path, got)
		}
	}
}

func TestImagesHTTPFailures(t *testing.T) {
	store := &fakeImageStore{body: make(map[int64]scheduledposts.Image)}
	h := imageTestMux(store)
	path := "/scheduled-posts/1/images"
	png := []byte("\x89PNG\r\n\x1a\nimage")
	for _, tt := range []struct {
		name string
		data []byte
		err  error
		want int
	}{
		{"empty", nil, nil, 400},
		{"too large", bytes.Repeat([]byte("x"), 65), nil, 413},
		{"spoofed type", []byte("<html>not an image</html>"), nil, 415},
		{"missing post", png, scheduledposts.ErrNotFound, 404},
		{"not editable", png, scheduledposts.ErrNotEditable, 409},
		{"count limit", png, scheduledposts.ErrImageLimit, 413},
		{"unexpected error", png, errors.New("database failure"), 500},
	} {
		t.Run(tt.name, func(t *testing.T) {
			store.err = tt.err
			res := imageRequest(h, "POST", path, tt.data, map[string]string{"Content-Type": "image/png"})
			if res.Code != tt.want {
				t.Fatalf("status = %d, want %d: %s", res.Code, tt.want, res.Body)
			}
			if tt.want == 500 && strings.Contains(res.Body.String(), "database failure") {
				t.Fatal("internal error leaked")
			}
		})
	}
	store.err = scheduledposts.ErrImageInUse
	if got := imageRequest(h, "DELETE", path+"/1", nil, nil).Code; got != http.StatusConflict {
		t.Fatalf("delete referenced image = %d", got)
	}
	if got := imageRequest(h, "GET", "/scheduled-posts/not-a-number/images/1", nil, nil).Code; got != http.StatusBadRequest {
		t.Fatalf("invalid post ID = %d", got)
	}
	if res := imageRequest(h, "POST", "/scheduled-posts/1/title-image", png, nil); res.Code != http.StatusMethodNotAllowed || res.Header().Get("Allow") == "" {
		t.Fatalf("wrong method = %d, Allow %q", res.Code, res.Header().Get("Allow"))
	}
	store.err = nil
	if got := imageRequest(h, "PUT", "/scheduled-posts/1/title-image/999", nil, nil).Code; got != http.StatusNotFound {
		t.Fatalf("select missing image = %d", got)
	}
}
