import { ScheduledPostStatus } from "./gen/lemmyscheduler/v1/scheduled_post_pb";

export const UPCOMING_STATUSES = [
  ScheduledPostStatus.DRAFT,
  ScheduledPostStatus.SCHEDULED,
  ScheduledPostStatus.PAUSED,
  ScheduledPostStatus.NEEDS_REVIEW,
  ScheduledPostStatus.FAILED,
];

export const HISTORY_STATUSES = [ScheduledPostStatus.POSTED];

const LABELS: Record<ScheduledPostStatus, string> = {
  [ScheduledPostStatus.UNSPECIFIED]: "Unknown",
  [ScheduledPostStatus.DRAFT]: "Draft",
  [ScheduledPostStatus.SCHEDULED]: "Scheduled",
  [ScheduledPostStatus.PAUSED]: "Paused",
  [ScheduledPostStatus.POSTING]: "Posting",
  [ScheduledPostStatus.POSTED]: "Posted",
  [ScheduledPostStatus.NEEDS_REVIEW]: "Needs review",
  [ScheduledPostStatus.FAILED]: "Failed",
};

export function statusLabel(status: ScheduledPostStatus): string {
  return LABELS[status] ?? "Unknown";
}

// CSS class suffix for the status badge, e.g. "needs-review".
export function statusClass(status: ScheduledPostStatus): string {
  return statusLabel(status).toLowerCase().replace(/ /g, "-");
}

// A status change offered as a button. `schedules` actions put the post on the
// schedule: they need a future time and return to the inbox afterwards.
export type StatusAction = { label: string; to: ScheduledPostStatus; schedules: boolean };

const ACTIONS: Partial<Record<ScheduledPostStatus, StatusAction[]>> = {
  [ScheduledPostStatus.DRAFT]: [{ label: "Schedule", to: ScheduledPostStatus.SCHEDULED, schedules: true }],
  [ScheduledPostStatus.SCHEDULED]: [
    { label: "Unschedule", to: ScheduledPostStatus.DRAFT, schedules: false },
    { label: "Pause", to: ScheduledPostStatus.PAUSED, schedules: false },
  ],
  [ScheduledPostStatus.PAUSED]: [{ label: "Resume", to: ScheduledPostStatus.SCHEDULED, schedules: true }],
};

export function statusActions(status: ScheduledPostStatus): StatusAction[] {
  return ACTIONS[status] ?? [];
}

// Mirrors scheduledposts.CanEdit; the server is authoritative.
export function isEditable(status: ScheduledPostStatus): boolean {
  return (
    status === ScheduledPostStatus.DRAFT ||
    status === ScheduledPostStatus.SCHEDULED ||
    status === ScheduledPostStatus.PAUSED
  );
}

export function readOnlyReason(status: ScheduledPostStatus): string {
  switch (status) {
    case ScheduledPostStatus.POSTING:
      return "This post is being published right now and can't be changed.";
    case ScheduledPostStatus.POSTED:
      return "This post has been published and can't be changed.";
    case ScheduledPostStatus.FAILED:
      return "This post failed to publish and can't be changed.";
    case ScheduledPostStatus.NEEDS_REVIEW:
      return "This post needs review before it can be changed.";
    default:
      return "This post can't be changed.";
  }
}
