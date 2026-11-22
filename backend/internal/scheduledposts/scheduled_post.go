// Package scheduledposts contains the scheduled-post domain model and rules.
package scheduledposts

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	MaxTitleLength = 200
	MaxBodyLength  = 10_000
)

type Status string

const (
	StatusDraft       Status = "draft"
	StatusScheduled   Status = "scheduled"
	StatusPaused      Status = "paused"
	StatusPosting     Status = "posting"
	StatusPosted      Status = "posted"
	StatusNeedsReview Status = "needs_review"
	StatusFailed      Status = "failed"
)

var (
	ErrNotFound     = errors.New("scheduled post not found")
	ErrNotEditable  = errors.New("scheduled post is not editable in this state")
	ErrNotDeletable = errors.New("scheduled post is not deletable in this state")
)

type ValidationError struct {
	Field   string
	Message string
}

func (e *ValidationError) Error() string {
	return fmt.Sprintf("%s: %s", e.Field, e.Message)
}

type ScheduledPost struct {
	ID           int64
	Title        string
	Body         string
	URL          string
	Community    string
	NSFW         bool
	Status       Status
	ScheduledAt  *time.Time
	PostedAt     *time.Time
	LemmyPostID  int64
	LemmyPostURL string
	CreatedAt    time.Time
	UpdatedAt    time.Time
	TitleImage   *ImageMetadata
	Images       []ImageMetadata
}

type AttemptOutcome string

const (
	AttemptStarted            AttemptOutcome = "started"
	AttemptPosted             AttemptOutcome = "posted"
	AttemptNeedsReview        AttemptOutcome = "needs_review"
	AttemptRecoveredOnStartup AttemptOutcome = "recovered_on_startup"
)

type Attempt struct {
	ID         int64
	PostID     int64
	Title      string
	StartedAt  time.Time
	FinishedAt *time.Time
	Outcome    AttemptOutcome
	Message    string
}

type ClaimedPost struct {
	Post      ScheduledPost
	AttemptID int64
}

type FinalizeAttempt struct {
	PostID       int64
	AttemptID    int64
	Outcome      AttemptOutcome
	Message      string // Safe-to-show text; never pass raw client errors or secrets.
	FinishedAt   time.Time
	LemmyPostID  int64
	LemmyPostURL string
}

type Cursor struct {
	SortTime time.Time
	ID       int64
}

type ListOptions struct {
	Statuses   []Status
	Descending bool
	Limit      int
	After      *Cursor
}

type Store interface {
	CreateScheduledPost(context.Context, ScheduledPost) (ScheduledPost, error)
	GetScheduledPost(context.Context, int64) (ScheduledPost, error)
	ListScheduledPosts(context.Context, ListOptions) ([]ScheduledPost, error)
	UpdateScheduledPost(context.Context, ScheduledPost) (ScheduledPost, error)
	DeleteScheduledPost(context.Context, int64) error
	// BulkUpdate applies the action to every eligible post in one transaction
	// and reports the rest as skipped. Eligibility (see BulkEligibleStatuses and
	// BulkResultingTime) is re-checked inside the write so concurrent changes,
	// such as the scheduler claiming a post, are skipped rather than modified.
	BulkUpdate(context.Context, BulkRequest) (BulkResult, error)
}

// SchedulerStore exposes scheduler-only writes. Implementations must serialize
// these operations through the process's publisher-ownership session.
type SchedulerStore interface {
	ClaimDue(context.Context, time.Time) (ClaimedPost, bool, error)
	FinalizeAttempt(context.Context, FinalizeAttempt) error
}

func CanEdit(status Status) bool {
	switch status {
	case StatusDraft, StatusScheduled, StatusPaused:
		return true
	default:
		return false
	}
}

func CanDelete(status Status) bool {
	return status != StatusPosting
}

func CanTransition(from, to Status) bool {
	if from == to {
		return from == StatusDraft || from == StatusScheduled || from == StatusPaused
	}
	switch from {
	case StatusDraft:
		return to == StatusScheduled
	case StatusScheduled:
		return to == StatusDraft || to == StatusPaused
	case StatusPaused:
		return to == StatusScheduled
	default:
		return false
	}
}

func ValidateCreate(post ScheduledPost, now time.Time) error {
	if post.Status != StatusDraft && post.Status != StatusScheduled && post.Status != StatusPaused {
		return invalid("status", "cannot be set by the user")
	}
	if err := validateFields(post); err != nil {
		return err
	}
	if len(BodyImageIDs(post.Body)) > 0 {
		return invalid("body", "cannot reference images before the post exists")
	}
	if post.Status == StatusScheduled && !isFuture(post.ScheduledAt, now) {
		return invalid("scheduled_at", "must be in the future when status is scheduled")
	}
	return nil
}

func ValidateUpdate(current, candidate ScheduledPost, now time.Time) error {
	if !CanEdit(current.Status) {
		return ErrNotEditable
	}
	if !CanTransition(current.Status, candidate.Status) {
		return invalid("status", "transition is not allowed")
	}
	if err := validateFields(candidate); err != nil {
		return err
	}
	if candidate.Status == StatusScheduled &&
		(current.Status != StatusScheduled || !sameTime(current.ScheduledAt, candidate.ScheduledAt)) &&
		!isFuture(candidate.ScheduledAt, now) {
		return invalid("scheduled_at", "must be in the future when status is scheduled")
	}
	return nil
}

func SortTime(post ScheduledPost) time.Time {
	if post.ScheduledAt != nil {
		return *post.ScheduledAt
	}
	return post.CreatedAt
}

func validateFields(post ScheduledPost) error {
	if strings.TrimSpace(post.Title) == "" {
		return invalid("title", "is required")
	}
	if utf8.RuneCountInString(post.Title) > MaxTitleLength {
		return invalid("title", fmt.Sprintf("must be at most %d characters", MaxTitleLength))
	}
	if utf8.RuneCountInString(post.Body) > MaxBodyLength {
		return invalid("body", fmt.Sprintf("must be at most %d characters", MaxBodyLength))
	}
	if len(BodyImageIDs(post.Body)) > MaxBodyImages {
		return invalid("body", fmt.Sprintf("must reference at most %d distinct images", MaxBodyImages))
	}
	if post.URL != "" {
		if post.TitleImage != nil {
			return invalid("url", "cannot be used with a title image")
		}
		u, err := url.Parse(post.URL)
		if err != nil || (!strings.EqualFold(u.Scheme, "http") && !strings.EqualFold(u.Scheme, "https")) || u.Host == "" || u.Opaque != "" {
			return invalid("url", "must be an absolute http or https URL")
		}
	}
	if strings.TrimSpace(post.Community) == "" {
		return invalid("community", "is required")
	}
	if post.Status != StatusDraft && post.Status != StatusScheduled && post.Status != StatusPaused &&
		post.Status != StatusPosting && post.Status != StatusPosted && post.Status != StatusNeedsReview && post.Status != StatusFailed {
		return invalid("status", "is unknown")
	}
	if (post.Status == StatusScheduled || post.Status == StatusPaused) && post.ScheduledAt == nil {
		return invalid("scheduled_at", "is required for scheduled or paused posts")
	}
	return nil
}

// The digit limit keeps every match within int64.
var bodyImagePattern = regexp.MustCompile(`\[\[image:([1-9][0-9]{0,17})\]\]`)

// BodyImageIDs returns the distinct image IDs referenced by [[image:<id>]]
// tokens in body, in order of first appearance.
func BodyImageIDs(body string) []int64 {
	var ids []int64
	seen := make(map[int64]struct{})
	for _, match := range bodyImagePattern.FindAllStringSubmatch(body, -1) {
		id, err := strconv.ParseInt(match[1], 10, 64)
		if err != nil {
			continue
		}
		if _, ok := seen[id]; !ok {
			seen[id] = struct{}{}
			ids = append(ids, id)
		}
	}
	return ids
}

func invalid(field, message string) error {
	return &ValidationError{Field: field, Message: message}
}

func isFuture(value *time.Time, now time.Time) bool {
	return value != nil && value.After(now)
}

func sameTime(a, b *time.Time) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return a.Equal(*b)
}
