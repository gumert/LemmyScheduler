package scheduledposts

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
)

func TestValidateCreate(t *testing.T) {
	now := time.Date(2026, 11, 15, 12, 0, 0, 0, time.UTC)
	future := now.Add(time.Hour)
	tests := []struct {
		name    string
		change  func(*ScheduledPost)
		wantErr string
	}{
		{name: "valid draft"},
		{name: "maximum title length", change: func(p *ScheduledPost) { p.Title = strings.Repeat("x", MaxTitleLength) }},
		{name: "maximum body length", change: func(p *ScheduledPost) { p.Body = strings.Repeat("x", MaxBodyLength) }},
		{name: "unicode characters counted as runes", change: func(p *ScheduledPost) { p.Title = strings.Repeat("é", MaxTitleLength) }},
		{name: "scheduled post in future", change: func(p *ScheduledPost) { p.Status, p.ScheduledAt = StatusScheduled, &future }},
		{name: "required title", change: func(p *ScheduledPost) { p.Title = " \t " }, wantErr: "title"},
		{name: "title too long", change: func(p *ScheduledPost) { p.Title = strings.Repeat("x", MaxTitleLength+1) }, wantErr: "title"},
		{name: "body too long", change: func(p *ScheduledPost) { p.Body = strings.Repeat("x", MaxBodyLength+1) }, wantErr: "body"},
		{name: "url must be http", change: func(p *ScheduledPost) { p.URL = "ftp://example.com/file" }, wantErr: "url"},
		{name: "url must be absolute", change: func(p *ScheduledPost) { p.URL = "https:/example.com" }, wantErr: "url"},
		{name: "required community", change: func(p *ScheduledPost) { p.Community = "" }, wantErr: "community"},
		{name: "scheduled time required", change: func(p *ScheduledPost) { p.Status = StatusPaused }, wantErr: "scheduled_at"},
		{name: "scheduled time must be future", change: func(p *ScheduledPost) { p.Status, p.ScheduledAt = StatusScheduled, &now }, wantErr: "scheduled_at"},
		{name: "scheduler-owned status rejected", change: func(p *ScheduledPost) { p.Status = StatusPosted }, wantErr: "status"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			post := validPost()
			if tt.change != nil {
				tt.change(&post)
			}
			err := ValidateCreate(post, now)
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("ValidateCreate() error = %v, want nil", err)
				}
				return
			}
			var validationErr *ValidationError
			if !errors.As(err, &validationErr) || validationErr.Field != tt.wantErr {
				t.Fatalf("ValidateCreate() error = %v, want validation error for %q", err, tt.wantErr)
			}
		})
	}
}

// SAFETY TEST: server validation rejects a URL and title image together on create and update.
func TestURLAndTitleImageExclusiveValidation(t *testing.T) {
	now := time.Now().UTC()
	create := validPost()
	create.URL = "https://example.test/article"
	create.TitleImage = &ImageMetadata{ID: 1}
	var validationErr *ValidationError
	if err := ValidateCreate(create, now); !errors.As(err, &validationErr) || validationErr.Field != "url" {
		t.Fatalf("create conflict = %v, want url field error", err)
	}

	current := validPost()
	current.TitleImage = &ImageMetadata{ID: 1}
	candidate := current
	candidate.URL = "https://example.test/article"
	if err := ValidateUpdate(current, candidate, now); !errors.As(err, &validationErr) || validationErr.Field != "url" {
		t.Fatalf("update conflict = %v, want url field error", err)
	}
}

func TestBodyImageLimit(t *testing.T) {
	post := validPost()
	for i := 1; i <= MaxBodyImages; i++ {
		post.Body += fmt.Sprintf("text [[image:%d]] ", i)
	}
	post.Body += "[[image:1]]"
	var validationErr *ValidationError
	if err := ValidateCreate(post, time.Now()); !errors.As(err, &validationErr) || validationErr.Field != "body" {
		t.Fatalf("create with image references: %v", err)
	}
	if err := ValidateUpdate(validPost(), post, time.Now()); err != nil {
		t.Fatalf("ten distinct images with a repeated reference: %v", err)
	}
	post.Body += "[[image:11]]"
	if err := ValidateUpdate(validPost(), post, time.Now()); !errors.As(err, &validationErr) || validationErr.Field != "body" {
		t.Fatalf("updating to eleven distinct images: %v", err)
	}
}

func TestValidateUpdateTransitions(t *testing.T) {
	now := time.Date(2026, 11, 15, 12, 0, 0, 0, time.UTC)
	future := now.Add(time.Hour)
	tests := []struct {
		name    string
		from    Status
		to      Status
		wantErr string
	}{
		{name: "draft unchanged", from: StatusDraft, to: StatusDraft},
		{name: "draft to scheduled", from: StatusDraft, to: StatusScheduled},
		{name: "scheduled to draft", from: StatusScheduled, to: StatusDraft},
		{name: "scheduled to paused", from: StatusScheduled, to: StatusPaused},
		{name: "paused to scheduled", from: StatusPaused, to: StatusScheduled},
		{name: "draft cannot jump to paused", from: StatusDraft, to: StatusPaused, wantErr: "status"},
		{name: "posted cannot be edited", from: StatusPosted, to: StatusPosted, wantErr: "scheduled post is not editable"},
		{name: "failed cannot be edited", from: StatusFailed, to: StatusFailed, wantErr: "scheduled post is not editable"},
		{name: "failed cannot be set by user", from: StatusDraft, to: StatusFailed, wantErr: "status"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			current := validPost()
			current.Status = tt.from
			candidate := current
			candidate.Status = tt.to
			if tt.from == StatusScheduled || tt.from == StatusPaused || tt.to == StatusScheduled || tt.to == StatusPaused {
				current.ScheduledAt = &future
				candidate.ScheduledAt = &future
			}

			err := ValidateUpdate(current, candidate, now)
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("ValidateUpdate() error = %v, want nil", err)
				}
				return
			}
			if !strings.Contains(errString(err), tt.wantErr) {
				t.Fatalf("ValidateUpdate() error = %v, want %q", err, tt.wantErr)
			}
		})
	}
}

// SAFETY TEST: a post the scheduler owns (posting) can never be edited or deleted by a user.
func TestCanEditAndDelete(t *testing.T) {
	tests := []struct {
		status    Status
		canEdit   bool
		canDelete bool
	}{
		{StatusDraft, true, true},
		{StatusScheduled, true, true},
		{StatusPaused, true, true},
		{StatusPosting, false, false},
		{StatusPosted, false, true},
		{StatusNeedsReview, false, true},
		{StatusFailed, false, true},
	}
	for _, tt := range tests {
		t.Run(string(tt.status), func(t *testing.T) {
			if got := CanEdit(tt.status); got != tt.canEdit {
				t.Errorf("CanEdit(%q) = %t, want %t", tt.status, got, tt.canEdit)
			}
			if got := CanDelete(tt.status); got != tt.canDelete {
				t.Errorf("CanDelete(%q) = %t, want %t", tt.status, got, tt.canDelete)
			}
		})
	}
}

func validPost() ScheduledPost {
	return ScheduledPost{
		Title:     "A title",
		Community: "rust",
		Status:    StatusDraft,
	}
}

func errString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

// SAFETY TEST: bulk actions never touch a posting row, and never resume an overdue post.
func TestCheckBulk(t *testing.T) {
	now := time.Date(2030, 1, 1, 12, 0, 0, 0, time.UTC)
	future := now.Add(time.Hour)
	past := now.Add(-time.Hour)
	tests := []struct {
		name    string
		request BulkRequest
		post    ScheduledPost
		want    SkipReason
	}{
		{"delete draft", BulkRequest{Action: BulkDelete}, ScheduledPost{Status: StatusDraft}, ""},
		{"delete posted", BulkRequest{Action: BulkDelete}, ScheduledPost{Status: StatusPosted}, ""},
		{"delete posting", BulkRequest{Action: BulkDelete}, ScheduledPost{Status: StatusPosting}, SkipWrongStatus},
		{"pause scheduled", BulkRequest{Action: BulkPause}, ScheduledPost{Status: StatusScheduled, ScheduledAt: &future}, ""},
		{"pause overdue scheduled", BulkRequest{Action: BulkPause}, ScheduledPost{Status: StatusScheduled, ScheduledAt: &past}, ""},
		{"pause paused", BulkRequest{Action: BulkPause}, ScheduledPost{Status: StatusPaused, ScheduledAt: &future}, SkipWrongStatus},
		{"pause draft", BulkRequest{Action: BulkPause}, ScheduledPost{Status: StatusDraft}, SkipWrongStatus},
		{"resume paused future", BulkRequest{Action: BulkResume}, ScheduledPost{Status: StatusPaused, ScheduledAt: &future}, ""},
		{"resume paused overdue", BulkRequest{Action: BulkResume}, ScheduledPost{Status: StatusPaused, ScheduledAt: &past}, SkipOverdue},
		{"resume scheduled", BulkRequest{Action: BulkResume}, ScheduledPost{Status: StatusScheduled, ScheduledAt: &future}, SkipWrongStatus},
		{"delay scheduled", BulkRequest{Action: BulkDelay, Delay: time.Hour}, ScheduledPost{Status: StatusScheduled, ScheduledAt: &future}, ""},
		{"delay paused", BulkRequest{Action: BulkDelay, Delay: time.Hour}, ScheduledPost{Status: StatusPaused, ScheduledAt: &future}, ""},
		{"delay overdue still past", BulkRequest{Action: BulkDelay, Delay: time.Minute}, ScheduledPost{Status: StatusScheduled, ScheduledAt: &past}, SkipOverdue},
		{"delay overdue into future", BulkRequest{Action: BulkDelay, Delay: 3 * time.Hour}, ScheduledPost{Status: StatusScheduled, ScheduledAt: &past}, ""},
		{"delay draft", BulkRequest{Action: BulkDelay, Delay: time.Hour}, ScheduledPost{Status: StatusDraft}, SkipWrongStatus},
		{"delay posting", BulkRequest{Action: BulkDelay, Delay: time.Hour}, ScheduledPost{Status: StatusPosting, ScheduledAt: &future}, SkipWrongStatus},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			skip, ok := CheckBulk(test.request, test.post, now)
			if ok != (test.want == "") || skip.Reason != test.want {
				t.Errorf("CheckBulk() = (%q, %v), want reason %q", skip.Reason, ok, test.want)
			}
		})
	}
}
