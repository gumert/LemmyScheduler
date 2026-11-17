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
		{name: "failed can be edited without changing state", from: StatusFailed, to: StatusFailed},
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
		{StatusFailed, true, true},
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
