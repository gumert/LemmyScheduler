package scheduledposts

import "time"

const (
	MaxBulkIDs   = 200
	MaxBulkDelay = 365 * 24 * time.Hour
)

type BulkAction string

const (
	BulkDelete BulkAction = "delete"
	BulkPause  BulkAction = "pause"
	BulkResume BulkAction = "resume"
	BulkDelay  BulkAction = "delay"
)

type SkipReason string

const (
	SkipNotFound    SkipReason = "not_found"
	SkipWrongStatus SkipReason = "wrong_status"
	SkipOverdue     SkipReason = "overdue"
)

type BulkRequest struct {
	IDs    []int64
	Action BulkAction
	Delay  time.Duration
}

type BulkSkip struct {
	ID      int64
	Reason  SkipReason
	Message string
}

type BulkResult struct {
	UpdatedIDs []int64
	Skipped    []BulkSkip
}

var allStatuses = []Status{
	StatusDraft, StatusScheduled, StatusPaused, StatusPosting,
	StatusPosted, StatusNeedsReview, StatusFailed,
}

// BulkEligibleStatuses returns the statuses the action may be applied to.
func BulkEligibleStatuses(action BulkAction) []Status {
	var statuses []Status
	for _, status := range allStatuses {
		if bulkStatusEligible(action, status) {
			statuses = append(statuses, status)
		}
	}
	return statuses
}

func bulkStatusEligible(action BulkAction, status Status) bool {
	switch action {
	case BulkDelete:
		return CanDelete(status)
	case BulkPause:
		return status != StatusPaused && CanTransition(status, StatusPaused)
	case BulkResume:
		return status == StatusPaused && CanTransition(status, StatusScheduled)
	case BulkDelay:
		return CanEdit(status) && (status == StatusScheduled || status == StatusPaused)
	default:
		return false
	}
}

// BulkResultingTime reports the scheduled time a row has after the action, and
// whether the action must leave that time in the future.
func BulkResultingTime(action BulkAction, scheduledAt *time.Time, delay time.Duration) (*time.Time, bool) {
	switch action {
	case BulkResume:
		return scheduledAt, true
	case BulkDelay:
		if scheduledAt == nil {
			return nil, true
		}
		shifted := scheduledAt.Add(delay)
		return &shifted, true
	default:
		return scheduledAt, false
	}
}

// CheckBulk returns why the action cannot be applied to post, or ok when it can.
func CheckBulk(request BulkRequest, post ScheduledPost, now time.Time) (BulkSkip, bool) {
	if !bulkStatusEligible(request.Action, post.Status) {
		return BulkSkip{ID: post.ID, Reason: SkipWrongStatus, Message: wrongStatusMessage(request.Action, post.Status)}, false
	}
	if resulting, mustBeFuture := BulkResultingTime(request.Action, post.ScheduledAt, request.Delay); mustBeFuture && !isFuture(resulting, now) {
		return BulkSkip{ID: post.ID, Reason: SkipOverdue, Message: overdueMessage(request.Action)}, false
	}
	return BulkSkip{}, true
}

func wrongStatusMessage(action BulkAction, status Status) string {
	switch action {
	case BulkDelete:
		return "a post that is being posted cannot be deleted"
	case BulkPause:
		return "only scheduled posts can be paused (this one is " + string(status) + ")"
	case BulkResume:
		return "only paused posts can be resumed (this one is " + string(status) + ")"
	default:
		return "only scheduled or paused posts can be delayed (this one is " + string(status) + ")"
	}
}

func overdueMessage(action BulkAction) string {
	if action == BulkResume {
		return "scheduled time has passed; set a new time before resuming"
	}
	return "scheduled time would still be in the past after the delay"
}

// ValidateBulk checks the request shape, deduplicating ids in order.
func ValidateBulk(request BulkRequest) (BulkRequest, error) {
	if len(request.IDs) == 0 {
		return request, invalid("ids", "must not be empty")
	}
	if len(request.IDs) > MaxBulkIDs {
		return request, invalid("ids", "must contain at most 200 ids")
	}
	seen := make(map[int64]struct{}, len(request.IDs))
	ids := make([]int64, 0, len(request.IDs))
	for _, id := range request.IDs {
		if id <= 0 {
			return request, invalid("ids", "must be positive")
		}
		if _, ok := seen[id]; !ok {
			seen[id] = struct{}{}
			ids = append(ids, id)
		}
	}
	request.IDs = ids
	switch request.Action {
	case BulkDelete, BulkPause, BulkResume:
		if request.Delay != 0 {
			return request, invalid("delay_seconds", "is only allowed for delay")
		}
	case BulkDelay:
		if request.Delay <= 0 {
			return request, invalid("delay_seconds", "must be greater than 0")
		}
		if request.Delay > MaxBulkDelay {
			return request, invalid("delay_seconds", "must be at most 365 days")
		}
	default:
		return request, invalid("action", "is unspecified or unknown")
	}
	return request, nil
}
