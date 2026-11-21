package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/gumert/LemmyScheduler/backend/internal/gen/lemmyscheduler/v1/lemmyschedulerv1connect"
	"github.com/gumert/LemmyScheduler/backend/internal/scheduledposts"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type fakeScheduledPostStore struct {
	posts     map[int64]scheduledposts.ScheduledPost
	nextID    int64
	listPages [][]scheduledposts.ScheduledPost
	listCalls []scheduledposts.ListOptions
	bulkCalls []scheduledposts.BulkRequest
}

func newFakeScheduledPostStore(posts ...scheduledposts.ScheduledPost) *fakeScheduledPostStore {
	store := &fakeScheduledPostStore{posts: make(map[int64]scheduledposts.ScheduledPost), nextID: 1}
	for _, post := range posts {
		store.posts[post.ID] = post
		if post.ID >= store.nextID {
			store.nextID = post.ID + 1
		}
	}
	return store
}

func (s *fakeScheduledPostStore) CreateScheduledPost(_ context.Context, post scheduledposts.ScheduledPost) (scheduledposts.ScheduledPost, error) {
	post.ID = s.nextID
	s.nextID++
	post.CreatedAt = time.Now().UTC()
	post.UpdatedAt = post.CreatedAt
	s.posts[post.ID] = post
	return post, nil
}

func (s *fakeScheduledPostStore) GetScheduledPost(_ context.Context, id int64) (scheduledposts.ScheduledPost, error) {
	post, ok := s.posts[id]
	if !ok {
		return scheduledposts.ScheduledPost{}, scheduledposts.ErrNotFound
	}
	return post, nil
}

func (s *fakeScheduledPostStore) ListScheduledPosts(_ context.Context, options scheduledposts.ListOptions) ([]scheduledposts.ScheduledPost, error) {
	s.listCalls = append(s.listCalls, options)
	pageIndex := len(s.listCalls) - 1
	if pageIndex >= len(s.listPages) {
		return nil, errors.New("fake scheduled post store: unexpected List call")
	}
	return s.listPages[pageIndex], nil
}

func (s *fakeScheduledPostStore) UpdateScheduledPost(_ context.Context, post scheduledposts.ScheduledPost) (scheduledposts.ScheduledPost, error) {
	current, ok := s.posts[post.ID]
	if !ok {
		return scheduledposts.ScheduledPost{}, scheduledposts.ErrNotFound
	}
	if !scheduledposts.CanEdit(current.Status) {
		return scheduledposts.ScheduledPost{}, scheduledposts.ErrNotEditable
	}
	current.Title = post.Title
	current.Body = post.Body
	current.URL = post.URL
	current.Community = post.Community
	current.NSFW = post.NSFW
	current.Status = post.Status
	current.ScheduledAt = post.ScheduledAt
	current.UpdatedAt = time.Now().UTC()
	s.posts[post.ID] = current
	return current, nil
}

func (s *fakeScheduledPostStore) DeleteScheduledPost(_ context.Context, id int64) error {
	post, ok := s.posts[id]
	if !ok {
		return scheduledposts.ErrNotFound
	}
	if !scheduledposts.CanDelete(post.Status) {
		return scheduledposts.ErrNotDeletable
	}
	delete(s.posts, id)
	return nil
}

func (s *fakeScheduledPostStore) BulkUpdate(_ context.Context, request scheduledposts.BulkRequest) (scheduledposts.BulkResult, error) {
	s.bulkCalls = append(s.bulkCalls, request)
	var result scheduledposts.BulkResult
	for _, id := range request.IDs {
		post, ok := s.posts[id]
		if !ok {
			result.Skipped = append(result.Skipped, scheduledposts.BulkSkip{ID: id, Reason: scheduledposts.SkipNotFound, Message: "not found"})
			continue
		}
		if skip, ok := scheduledposts.CheckBulk(request, post, time.Now()); !ok {
			result.Skipped = append(result.Skipped, skip)
			continue
		}
		result.UpdatedIDs = append(result.UpdatedIDs, id)
	}
	return result, nil
}

func scheduledPostHandler(store scheduledposts.Store) http.Handler {
	path, handler := lemmyschedulerv1connect.NewScheduledPostServiceHandler(NewScheduledPostServer(store))
	mux := http.NewServeMux()
	mux.Handle(path, handler)
	return mux
}

func postConnectJSON(t *testing.T, handler http.Handler, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	res := httptest.NewRecorder()
	handler.ServeHTTP(res, req)
	return res
}

func TestScheduledPostCreateAndGetOverConnectJSON(t *testing.T) {
	store := newFakeScheduledPostStore()
	handler := scheduledPostHandler(store)

	created := postConnectJSON(t, handler, lemmyschedulerv1connect.ScheduledPostServiceCreateScheduledPostProcedure,
		`{"scheduledPost":{"title":"A title","community":"rust"}}`)
	require.Equal(t, http.StatusOK, created.Code, "create response: %s", created.Body)
	var createResponse struct {
		ScheduledPost struct {
			ID     int64  `json:"id,string"`
			Status string `json:"status"`
		} `json:"scheduledPost"`
	}
	require.NoError(t, json.Unmarshal(created.Body.Bytes(), &createResponse))
	assert.Equal(t, int64(1), createResponse.ScheduledPost.ID)
	assert.Equal(t, "SCHEDULED_POST_STATUS_DRAFT", createResponse.ScheduledPost.Status)

	got := postConnectJSON(t, handler, lemmyschedulerv1connect.ScheduledPostServiceGetScheduledPostProcedure, `{"id":"1"}`)
	require.Equal(t, http.StatusOK, got.Code, "get response: %s", got.Body)
	assert.Contains(t, got.Body.String(), `"title":"A title"`)
}

func TestScheduledPostUpdateAndDelete(t *testing.T) {
	current := scheduledposts.ScheduledPost{
		ID: 3, Title: "before", Community: "rust", Status: scheduledposts.StatusDraft,
	}
	store := newFakeScheduledPostStore(current)
	handler := scheduledPostHandler(store)
	update := postConnectJSON(t, handler, lemmyschedulerv1connect.ScheduledPostServiceUpdateScheduledPostProcedure,
		`{"id":"3","scheduledPost":{"title":"after","community":"rust"}}`)
	require.Equal(t, http.StatusOK, update.Code, "update response: %s", update.Body)
	assert.Contains(t, update.Body.String(), `"title":"after"`)
	deleted := postConnectJSON(t, handler, lemmyschedulerv1connect.ScheduledPostServiceDeleteScheduledPostProcedure, `{"id":"3"}`)
	require.Equal(t, http.StatusOK, deleted.Code, "delete response: %s", deleted.Body)
	_, err := store.GetScheduledPost(context.Background(), current.ID)
	assert.ErrorIs(t, err, scheduledposts.ErrNotFound)
}

func TestScheduledPostDeleteAndValidationErrorCodes(t *testing.T) {
	store := newFakeScheduledPostStore(scheduledposts.ScheduledPost{
		ID: 4, Title: "claimed", Community: "rust", Status: scheduledposts.StatusPosting,
	})
	res := postConnectJSON(t, scheduledPostHandler(store),
		lemmyschedulerv1connect.ScheduledPostServiceDeleteScheduledPostProcedure, `{"id":"4"}`)
	assert.Contains(t, res.Body.String(), "failed_precondition")

	invalid := newFakeScheduledPostStore()
	res = postConnectJSON(t, scheduledPostHandler(invalid),
		lemmyschedulerv1connect.ScheduledPostServiceCreateScheduledPostProcedure,
		`{"scheduledPost":{"title":"","community":"rust"}}`)
	assert.Equal(t, http.StatusBadRequest, res.Code, "create response: %s", res.Body)
	assert.Contains(t, res.Body.String(), "invalid_argument")
}

func TestScheduledPostListPageTokenRoundTrip(t *testing.T) {
	sortTime := time.Date(2026, 11, 15, 12, 0, 0, 123000000, time.UTC)
	first := scheduledposts.ScheduledPost{
		ID: 11, Title: "first", Community: "rust", Status: scheduledposts.StatusPosted,
		CreatedAt: sortTime, UpdatedAt: sortTime,
	}
	second := scheduledposts.ScheduledPost{
		ID: 10, Title: "second", Community: "rust", Status: scheduledposts.StatusPosted,
		CreatedAt: sortTime, UpdatedAt: sortTime,
	}
	third := scheduledposts.ScheduledPost{
		ID: 9, Title: "third", Community: "rust", Status: scheduledposts.StatusPosted,
		CreatedAt: sortTime, UpdatedAt: sortTime,
	}
	store := newFakeScheduledPostStore()
	store.listPages = [][]scheduledposts.ScheduledPost{{first, second}, {second, third}}
	handler := scheduledPostHandler(store)
	firstPage := postConnectJSON(t, handler, lemmyschedulerv1connect.ScheduledPostServiceListScheduledPostsProcedure,
		`{"statuses":["SCHEDULED_POST_STATUS_POSTED"],"descending":true,"pageSize":1}`)
	require.Equal(t, http.StatusOK, firstPage.Code, "first list response: %s", firstPage.Body)
	var page struct {
		ScheduledPosts []json.RawMessage `json:"scheduledPosts"`
		NextPageToken  string            `json:"nextPageToken"`
	}
	require.NoError(t, json.Unmarshal(firstPage.Body.Bytes(), &page))
	require.Len(t, page.ScheduledPosts, 1)
	require.NotEmpty(t, page.NextPageToken)
	token, err := decodeScheduledPostPageToken(page.NextPageToken)
	require.NoError(t, err)
	assert.Equal(t, first.ID, token.ID)
	assert.True(t, token.SortTime.Equal(sortTime))
	require.Len(t, store.listCalls, 1)
	assert.Equal(t, 2, store.listCalls[0].Limit)
	assert.True(t, store.listCalls[0].Descending)
	assert.Equal(t, []scheduledposts.Status{scheduledposts.StatusPosted}, store.listCalls[0].Statuses)
	assert.Nil(t, store.listCalls[0].After)

	body, err := json.Marshal(map[string]any{
		"statuses": []string{"SCHEDULED_POST_STATUS_POSTED"}, "descending": true,
		"pageSize": 1, "pageToken": page.NextPageToken,
	})
	require.NoError(t, err)
	nextPage := postConnectJSON(t, handler, lemmyschedulerv1connect.ScheduledPostServiceListScheduledPostsProcedure, string(body))
	require.Equal(t, http.StatusOK, nextPage.Code, "second list response: %s", nextPage.Body)
	var secondPage struct {
		ScheduledPosts []struct {
			ID int64 `json:"id,string"`
		} `json:"scheduledPosts"`
	}
	require.NoError(t, json.Unmarshal(nextPage.Body.Bytes(), &secondPage))
	require.Len(t, secondPage.ScheduledPosts, 1)
	assert.Equal(t, second.ID, secondPage.ScheduledPosts[0].ID)
	require.Len(t, store.listCalls, 2)
	got := store.listCalls[1]
	require.NotNil(t, got.After)
	assert.Equal(t, first.ID, got.After.ID)
	assert.True(t, got.After.SortTime.Equal(sortTime))
}

func TestScheduledPostNotFoundErrorMapping(t *testing.T) {
	store := newFakeScheduledPostStore()
	res := postConnectJSON(t, scheduledPostHandler(store),
		lemmyschedulerv1connect.ScheduledPostServiceGetScheduledPostProcedure, `{"id":"9"}`)
	assert.Equal(t, http.StatusNotFound, res.Code, "get response: %s", res.Body)
	assert.Contains(t, res.Body.String(), "not_found")
	assert.Equal(t, connect.CodeInternal, connect.CodeOf(scheduledPostError(errors.New("unexpected"))))
}

var _ scheduledposts.Store = (*fakeScheduledPostStore)(nil)

func TestBulkUpdateScheduledPostsValidation(t *testing.T) {
	ids := make([]string, 201)
	for i := range ids {
		ids[i] = "1"
	}
	bodies := map[string]string{
		"empty ids":       `{"ids":[],"action":"BULK_ACTION_PAUSE"}`,
		"too many ids":    `{"ids":[` + strings.Join(ids, ",") + `],"action":"BULK_ACTION_PAUSE"}`,
		"non-positive id": `{"ids":[0],"action":"BULK_ACTION_PAUSE"}`,
		"unspecified":     `{"ids":[1]}`,
		"delay missing":   `{"ids":[1],"action":"BULK_ACTION_DELAY"}`,
		"delay negative":  `{"ids":[1],"action":"BULK_ACTION_DELAY","delaySeconds":"-5"}`,
		"delay too large": `{"ids":[1],"action":"BULK_ACTION_DELAY","delaySeconds":"31536001"}`,
		"delay on pause":  `{"ids":[1],"action":"BULK_ACTION_PAUSE","delaySeconds":"60"}`,
	}
	for name, body := range bodies {
		t.Run(name, func(t *testing.T) {
			store := newFakeScheduledPostStore()
			res := postConnectJSON(t, scheduledPostHandler(store), "/lemmyscheduler.v1.ScheduledPostService/BulkUpdateScheduledPosts", body)
			assert.Equal(t, http.StatusBadRequest, res.Code, res.Body.String())
			assert.Empty(t, store.bulkCalls)
		})
	}
}

func TestBulkUpdateScheduledPostsResponseShape(t *testing.T) {
	future := time.Now().UTC().Add(time.Hour)
	past := time.Now().UTC().Add(-time.Hour)
	store := newFakeScheduledPostStore(
		scheduledposts.ScheduledPost{ID: 1, Status: scheduledposts.StatusPaused, ScheduledAt: &future},
		scheduledposts.ScheduledPost{ID: 2, Status: scheduledposts.StatusPaused, ScheduledAt: &past},
		scheduledposts.ScheduledPost{ID: 3, Status: scheduledposts.StatusDraft},
	)
	res := postConnectJSON(t, scheduledPostHandler(store), "/lemmyscheduler.v1.ScheduledPostService/BulkUpdateScheduledPosts",
		`{"ids":[1,2,3,3,4],"action":"BULK_ACTION_RESUME"}`)
	require.Equal(t, http.StatusOK, res.Code, res.Body.String())

	var body struct {
		UpdatedIDs []string `json:"updatedIds"`
		Skipped    []struct {
			ID     string `json:"id"`
			Reason string `json:"reason"`
		} `json:"skipped"`
	}
	require.NoError(t, json.Unmarshal(res.Body.Bytes(), &body))
	assert.Equal(t, []string{"1"}, body.UpdatedIDs)
	require.Len(t, body.Skipped, 3)
	assert.Equal(t, "BULK_SKIP_REASON_OVERDUE", body.Skipped[0].Reason)
	assert.Equal(t, "BULK_SKIP_REASON_WRONG_STATUS", body.Skipped[1].Reason)
	assert.Equal(t, "BULK_SKIP_REASON_NOT_FOUND", body.Skipped[2].Reason)
	assert.Equal(t, []int64{1, 2, 3, 4}, store.bulkCalls[0].IDs)
}
