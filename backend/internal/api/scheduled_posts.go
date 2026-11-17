package api

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sort"
	"time"

	"connectrpc.com/connect"
	lemmyschedulerv1 "github.com/gumert/LemmyScheduler/backend/internal/gen/lemmyscheduler/v1"
	"github.com/gumert/LemmyScheduler/backend/internal/gen/lemmyscheduler/v1/lemmyschedulerv1connect"
	"github.com/gumert/LemmyScheduler/backend/internal/scheduledposts"
	"google.golang.org/protobuf/types/known/timestamppb"
)

const (
	defaultScheduledPostPageSize = 50
	maxScheduledPostPageSize     = 200
)

type ScheduledPostServer struct {
	store scheduledposts.Store
	now   func() time.Time
}

var _ lemmyschedulerv1connect.ScheduledPostServiceHandler = (*ScheduledPostServer)(nil)

func NewScheduledPostServer(store scheduledposts.Store) *ScheduledPostServer {
	return &ScheduledPostServer{store: store, now: time.Now}
}

func (s *ScheduledPostServer) CreateScheduledPost(ctx context.Context, req *connect.Request[lemmyschedulerv1.CreateScheduledPostRequest]) (*connect.Response[lemmyschedulerv1.CreateScheduledPostResponse], error) {
	if req.Msg.GetScheduledPost() == nil {
		return nil, invalidArgument("scheduled_post is required")
	}
	post, err := postFromInput(req.Msg.GetScheduledPost(), scheduledposts.StatusDraft)
	if err != nil {
		return nil, invalidArgument(err.Error())
	}
	if err := scheduledposts.ValidateCreate(post, s.now()); err != nil {
		return nil, scheduledPostError(err)
	}
	created, err := s.store.CreateScheduledPost(ctx, post)
	if err != nil {
		return nil, scheduledPostError(err)
	}
	return connect.NewResponse(&lemmyschedulerv1.CreateScheduledPostResponse{ScheduledPost: postToProto(created)}), nil
}

func (s *ScheduledPostServer) GetScheduledPost(ctx context.Context, req *connect.Request[lemmyschedulerv1.GetScheduledPostRequest]) (*connect.Response[lemmyschedulerv1.GetScheduledPostResponse], error) {
	if req.Msg.GetId() <= 0 {
		return nil, invalidArgument("id must be positive")
	}
	post, err := s.store.GetScheduledPost(ctx, req.Msg.GetId())
	if err != nil {
		return nil, scheduledPostError(err)
	}
	return connect.NewResponse(&lemmyschedulerv1.GetScheduledPostResponse{ScheduledPost: postToProto(post)}), nil
}

func (s *ScheduledPostServer) ListScheduledPosts(ctx context.Context, req *connect.Request[lemmyschedulerv1.ListScheduledPostsRequest]) (*connect.Response[lemmyschedulerv1.ListScheduledPostsResponse], error) {
	pageSize := int(req.Msg.GetPageSize())
	if pageSize < 0 {
		return nil, invalidArgument("page_size cannot be negative")
	}
	if pageSize == 0 {
		pageSize = defaultScheduledPostPageSize
	}
	if pageSize > maxScheduledPostPageSize {
		return nil, invalidArgument(fmt.Sprintf("page_size must not exceed %d", maxScheduledPostPageSize))
	}

	statuses, err := statusesFromProto(req.Msg.GetStatuses())
	if err != nil {
		return nil, invalidArgument(err.Error())
	}
	tokenStatuses := statusStrings(statuses)
	var after *scheduledposts.Cursor
	if token := req.Msg.GetPageToken(); token != "" {
		decoded, err := decodeScheduledPostPageToken(token)
		if err != nil {
			return nil, invalidArgument(err.Error())
		}
		if decoded.Descending != req.Msg.GetDescending() || !slices.Equal(decoded.Statuses, tokenStatuses) {
			return nil, invalidArgument("page_token does not match the requested sort order and statuses")
		}
		after = &scheduledposts.Cursor{SortTime: decoded.SortTime, ID: decoded.ID}
	}

	posts, err := s.store.ListScheduledPosts(ctx, scheduledposts.ListOptions{
		Statuses: statuses, Descending: req.Msg.GetDescending(), Limit: pageSize + 1, After: after,
	})
	if err != nil {
		return nil, scheduledPostError(err)
	}
	response := &lemmyschedulerv1.ListScheduledPostsResponse{}
	if len(posts) > pageSize {
		posts = posts[:pageSize]
		last := posts[len(posts)-1]
		response.NextPageToken, err = encodeScheduledPostPageToken(pageToken{
			FormatVersion: 1, SortTime: scheduledposts.SortTime(last), ID: last.ID,
			Descending: req.Msg.GetDescending(), Statuses: tokenStatuses,
		})
		if err != nil {
			return nil, connect.NewError(connect.CodeInternal, err)
		}
	}
	response.ScheduledPosts = make([]*lemmyschedulerv1.ScheduledPost, 0, len(posts))
	for _, post := range posts {
		response.ScheduledPosts = append(response.ScheduledPosts, postToProto(post))
	}
	return connect.NewResponse(response), nil
}

func (s *ScheduledPostServer) UpdateScheduledPost(ctx context.Context, req *connect.Request[lemmyschedulerv1.UpdateScheduledPostRequest]) (*connect.Response[lemmyschedulerv1.UpdateScheduledPostResponse], error) {
	if req.Msg.GetId() <= 0 {
		return nil, invalidArgument("id must be positive")
	}
	if req.Msg.GetScheduledPost() == nil {
		return nil, invalidArgument("scheduled_post is required")
	}
	current, err := s.store.GetScheduledPost(ctx, req.Msg.GetId())
	if err != nil {
		return nil, scheduledPostError(err)
	}
	candidate, err := postFromInput(req.Msg.GetScheduledPost(), current.Status)
	if err != nil {
		return nil, invalidArgument(err.Error())
	}
	candidate.ID = current.ID
	candidate.PostedAt = current.PostedAt
	candidate.LemmyPostURL = current.LemmyPostURL
	candidate.CreatedAt = current.CreatedAt
	if err := scheduledposts.ValidateUpdate(current, candidate, s.now()); err != nil {
		return nil, scheduledPostError(err)
	}
	updated, err := s.store.UpdateScheduledPost(ctx, candidate)
	if err != nil {
		return nil, scheduledPostError(err)
	}
	return connect.NewResponse(&lemmyschedulerv1.UpdateScheduledPostResponse{ScheduledPost: postToProto(updated)}), nil
}

func (s *ScheduledPostServer) DeleteScheduledPost(ctx context.Context, req *connect.Request[lemmyschedulerv1.DeleteScheduledPostRequest]) (*connect.Response[lemmyschedulerv1.DeleteScheduledPostResponse], error) {
	if req.Msg.GetId() <= 0 {
		return nil, invalidArgument("id must be positive")
	}
	if err := s.store.DeleteScheduledPost(ctx, req.Msg.GetId()); err != nil {
		return nil, scheduledPostError(err)
	}
	return connect.NewResponse(&lemmyschedulerv1.DeleteScheduledPostResponse{}), nil
}

type pageToken struct {
	FormatVersion int       `json:"v"`
	SortTime      time.Time `json:"t"`
	ID            int64     `json:"i"`
	Descending    bool      `json:"d"`
	Statuses      []string  `json:"s"`
}

func encodeScheduledPostPageToken(token pageToken) (string, error) {
	data, err := json.Marshal(token)
	if err != nil {
		return "", fmt.Errorf("encode page token: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(data), nil
}

func decodeScheduledPostPageToken(value string) (pageToken, error) {
	data, err := base64.RawURLEncoding.DecodeString(value)
	if err != nil {
		return pageToken{}, errors.New("page_token is malformed")
	}
	var token pageToken
	if err := json.Unmarshal(data, &token); err != nil || token.FormatVersion != 1 || token.ID <= 0 || token.SortTime.IsZero() {
		return pageToken{}, errors.New("page_token is invalid")
	}
	token.SortTime = token.SortTime.UTC()
	return token, nil
}

func postFromInput(input *lemmyschedulerv1.ScheduledPostInput, defaultStatus scheduledposts.Status) (scheduledposts.ScheduledPost, error) {
	status := defaultStatus
	if input.GetStatus() != lemmyschedulerv1.ScheduledPostStatus_SCHEDULED_POST_STATUS_UNSPECIFIED {
		var err error
		status, err = statusFromProto(input.GetStatus())
		if err != nil {
			return scheduledposts.ScheduledPost{}, err
		}
	}
	var scheduledAt *time.Time
	if timestamp := input.GetScheduledAt(); timestamp != nil {
		if err := timestamp.CheckValid(); err != nil {
			return scheduledposts.ScheduledPost{}, fmt.Errorf("scheduled_at: %w", err)
		}
		value := timestamp.AsTime().UTC()
		scheduledAt = &value
	}
	return scheduledposts.ScheduledPost{
		Title: input.GetTitle(), Body: input.GetBody(), URL: input.GetUrl(),
		Community: input.GetCommunity(), NSFW: input.GetNsfw(), Status: status,
		ScheduledAt: scheduledAt,
	}, nil
}

func statusFromProto(status lemmyschedulerv1.ScheduledPostStatus) (scheduledposts.Status, error) {
	switch status {
	case lemmyschedulerv1.ScheduledPostStatus_SCHEDULED_POST_STATUS_DRAFT:
		return scheduledposts.StatusDraft, nil
	case lemmyschedulerv1.ScheduledPostStatus_SCHEDULED_POST_STATUS_SCHEDULED:
		return scheduledposts.StatusScheduled, nil
	case lemmyschedulerv1.ScheduledPostStatus_SCHEDULED_POST_STATUS_PAUSED:
		return scheduledposts.StatusPaused, nil
	case lemmyschedulerv1.ScheduledPostStatus_SCHEDULED_POST_STATUS_POSTING:
		return scheduledposts.StatusPosting, nil
	case lemmyschedulerv1.ScheduledPostStatus_SCHEDULED_POST_STATUS_POSTED:
		return scheduledposts.StatusPosted, nil
	case lemmyschedulerv1.ScheduledPostStatus_SCHEDULED_POST_STATUS_NEEDS_REVIEW:
		return scheduledposts.StatusNeedsReview, nil
	case lemmyschedulerv1.ScheduledPostStatus_SCHEDULED_POST_STATUS_FAILED:
		return scheduledposts.StatusFailed, nil
	default:
		return "", fmt.Errorf("status is unspecified or unknown")
	}
}

func statusesFromProto(values []lemmyschedulerv1.ScheduledPostStatus) ([]scheduledposts.Status, error) {
	statuses := make([]scheduledposts.Status, 0, len(values))
	for _, value := range values {
		status, err := statusFromProto(value)
		if err != nil {
			return nil, err
		}
		statuses = append(statuses, status)
	}
	sort.Slice(statuses, func(i, j int) bool { return statuses[i] < statuses[j] })
	return slices.Compact(statuses), nil
}

func statusStrings(statuses []scheduledposts.Status) []string {
	values := make([]string, len(statuses))
	for i, status := range statuses {
		values[i] = string(status)
	}
	return values
}

func statusToProto(status scheduledposts.Status) lemmyschedulerv1.ScheduledPostStatus {
	switch status {
	case scheduledposts.StatusDraft:
		return lemmyschedulerv1.ScheduledPostStatus_SCHEDULED_POST_STATUS_DRAFT
	case scheduledposts.StatusScheduled:
		return lemmyschedulerv1.ScheduledPostStatus_SCHEDULED_POST_STATUS_SCHEDULED
	case scheduledposts.StatusPaused:
		return lemmyschedulerv1.ScheduledPostStatus_SCHEDULED_POST_STATUS_PAUSED
	case scheduledposts.StatusPosting:
		return lemmyschedulerv1.ScheduledPostStatus_SCHEDULED_POST_STATUS_POSTING
	case scheduledposts.StatusPosted:
		return lemmyschedulerv1.ScheduledPostStatus_SCHEDULED_POST_STATUS_POSTED
	case scheduledposts.StatusNeedsReview:
		return lemmyschedulerv1.ScheduledPostStatus_SCHEDULED_POST_STATUS_NEEDS_REVIEW
	case scheduledposts.StatusFailed:
		return lemmyschedulerv1.ScheduledPostStatus_SCHEDULED_POST_STATUS_FAILED
	default:
		return lemmyschedulerv1.ScheduledPostStatus_SCHEDULED_POST_STATUS_UNSPECIFIED
	}
}

func postToProto(post scheduledposts.ScheduledPost) *lemmyschedulerv1.ScheduledPost {
	result := &lemmyschedulerv1.ScheduledPost{
		Id: post.ID, Title: post.Title, Body: post.Body, Url: post.URL, Community: post.Community,
		Nsfw: post.NSFW, Status: statusToProto(post.Status), LemmyPostUrl: post.LemmyPostURL,
		CreatedAt: timestamppb.New(post.CreatedAt), UpdatedAt: timestamppb.New(post.UpdatedAt),
	}
	if post.ScheduledAt != nil {
		result.ScheduledAt = timestamppb.New(*post.ScheduledAt)
	}
	if post.PostedAt != nil {
		result.PostedAt = timestamppb.New(*post.PostedAt)
	}
	if post.TitleImage != nil {
		result.TitleImage = imageToProto(*post.TitleImage)
	}
	for _, image := range post.Images {
		result.Images = append(result.Images, imageToProto(image))
	}
	return result
}

func imageToProto(image scheduledposts.ImageMetadata) *lemmyschedulerv1.PostImage {
	return &lemmyschedulerv1.PostImage{
		Id: image.ID, ContentType: image.ContentType, CreatedAt: timestamppb.New(image.CreatedAt),
	}
}

func invalidArgument(message string) error {
	return connect.NewError(connect.CodeInvalidArgument, errors.New(message))
}

func scheduledPostError(err error) error {
	var validationErr *scheduledposts.ValidationError
	switch {
	case errors.As(err, &validationErr):
		return connect.NewError(connect.CodeInvalidArgument, err)
	case errors.Is(err, scheduledposts.ErrNotFound):
		return connect.NewError(connect.CodeNotFound, err)
	case errors.Is(err, scheduledposts.ErrNotEditable), errors.Is(err, scheduledposts.ErrNotDeletable):
		return connect.NewError(connect.CodeFailedPrecondition, err)
	default:
		return connect.NewError(connect.CodeInternal, err)
	}
}
