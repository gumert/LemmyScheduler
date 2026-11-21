import { BulkSkipReason, type BulkSkippedPost } from "./gen/lemmyscheduler/v1/scheduled_post_pb";

export type Selection = ReadonlySet<bigint>;

export type SelectionAction =
  | { type: "toggle"; id: bigint }
  | { type: "setAll"; ids: bigint[]; selected: boolean }
  | { type: "retain"; ids: bigint[] }
  | { type: "clear" };

export const EMPTY_SELECTION: Selection = new Set();

// Returns the same set when nothing changes so React can skip a re-render.
export function selectionReducer(state: Selection, action: SelectionAction): Selection {
  switch (action.type) {
    case "toggle": {
      const next = new Set(state);
      if (!next.delete(action.id)) {
        next.add(action.id);
      }
      return next;
    }
    case "setAll": {
      const next = new Set(state);
      for (const id of action.ids) {
        if (action.selected) {
          next.add(id);
        } else {
          next.delete(id);
        }
      }
      return next.size === state.size ? state : next;
    }
    case "retain": {
      const keep = new Set(action.ids);
      const next = new Set([...state].filter((id) => keep.has(id)));
      return next.size === state.size ? state : next;
    }
    case "clear":
      return state.size === 0 ? state : EMPTY_SELECTION;
  }
}

// The server accepts at most 365 days.
export const MAX_DELAY_SECONDS = 365 * 24 * 3600;

export type DelayUnit = "hours" | "days";

// Converts a custom delay to seconds. Returns undefined unless the amount is a
// positive whole number within the server's limit.
export function customDelaySeconds(amount: string, unit: DelayUnit): number | undefined {
  if (!/^\d{1,6}$/.test(amount.trim())) {
    return undefined;
  }
  const seconds = Number(amount) * (unit === "hours" ? 3600 : 86_400);
  return seconds > 0 && seconds <= MAX_DELAY_SECONDS ? seconds : undefined;
}

const REASON_LABELS: Record<BulkSkipReason, string> = {
  [BulkSkipReason.UNSPECIFIED]: "Skipped",
  [BulkSkipReason.NOT_FOUND]: "No longer exists",
  [BulkSkipReason.WRONG_STATUS]: "Not in a state that allows this",
  [BulkSkipReason.OVERDUE]: "Scheduled time has passed",
};

export type SkipGroup = { reason: BulkSkipReason; label: string; entries: { id: bigint; title: string }[] };

// Groups skipped posts by reason, in order of first appearance. `titles` maps
// post ids to titles; ids that are no longer listed fall back to "Post <id>".
export function groupSkips(skipped: BulkSkippedPost[], titles: ReadonlyMap<bigint, string>): SkipGroup[] {
  const groups = new Map<BulkSkipReason, SkipGroup>();
  for (const skip of skipped) {
    let group = groups.get(skip.reason);
    if (!group) {
      group = { reason: skip.reason, label: REASON_LABELS[skip.reason] ?? "Skipped", entries: [] };
      groups.set(skip.reason, group);
    }
    group.entries.push({ id: skip.id, title: titles.get(skip.id) ?? `Post ${skip.id}` });
  }
  return [...groups.values()];
}

export function summarizeBulk(updated: number, skipped: number): string {
  return `${updated} updated, ${skipped} skipped`;
}

// The server rejects larger requests.
export const MAX_BULK_IDS = 200;
