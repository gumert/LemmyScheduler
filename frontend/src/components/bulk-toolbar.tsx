"use client";

import { useState } from "react";

import { customDelaySeconds, MAX_BULK_IDS, summarizeBulk, type DelayUnit, type SkipGroup } from "../bulk";
import { BulkAction } from "../gen/lemmyscheduler/v1/scheduled_post_pb";

export type BulkResultView = { updated: number; skipped: number; groups: SkipGroup[] };

export function BulkToolbar({
  count,
  busy,
  onRun,
}: {
  count: number;
  busy: boolean;
  onRun: (action: BulkAction, delaySeconds?: number) => void;
}) {
  const [amount, setAmount] = useState("");
  const [unit, setUnit] = useState<DelayUnit>("hours");
  const custom = customDelaySeconds(amount, unit);
  const tooMany = count > MAX_BULK_IDS;
  const disabled = busy || count === 0 || tooMany;

  return (
    <div className="bulk-toolbar" role="toolbar" aria-label="Bulk actions">
      <button type="button" className="button danger" disabled={disabled} onClick={() => onRun(BulkAction.DELETE)}>
        Delete
      </button>
      <button type="button" className="button" disabled={disabled} onClick={() => onRun(BulkAction.PAUSE)}>
        Pause
      </button>
      <button type="button" className="button" disabled={disabled} onClick={() => onRun(BulkAction.RESUME)}>
        Resume
      </button>
      <form
        className="inline delay-group"
        onSubmit={(event) => {
          event.preventDefault();
          if (!disabled && custom !== undefined) {
            onRun(BulkAction.DELAY, custom);
          }
        }}
      >
        <input
          type="text"
          inputMode="numeric"
          pattern="[0-9]*"
          size={5}
          placeholder="Delay by"
          aria-label="Delay amount"
          value={amount}
          onKeyDown={(event) => {
            // Only digits may be typed; editing, navigation and shortcuts still work.
            if (event.key.length === 1 && !event.ctrlKey && !event.metaKey && !/^\d$/.test(event.key)) {
              event.preventDefault();
            }
          }}
          onChange={(event) => setAmount(event.target.value.replace(/\D/g, ""))}
        />
        <select aria-label="Delay unit" value={unit} onChange={(event) => setUnit(event.target.value as DelayUnit)}>
          <option value="hours">hours</option>
          <option value="days">days</option>
        </select>
        <button type="submit" className="button" disabled={disabled || custom === undefined}>
          Delay
        </button>
      </form>
      {tooMany && <span className="muted">Select at most {MAX_BULK_IDS} posts at a time.</span>}
    </div>
  );
}

export function BulkResult({ result, onDismiss }: { result: BulkResultView; onDismiss: () => void }) {
  return (
    <div className="state bulk-result" role="status">
      <p>
        <strong>{summarizeBulk(result.updated, result.skipped)}</strong>{" "}
        <button type="button" className="button" onClick={onDismiss}>
          Dismiss
        </button>
      </p>
      {result.groups.map((group) => (
        <div key={group.reason}>
          <p>
            {group.label} ({group.entries.length}):
          </p>
          <ul>
            {group.entries.map((entry) => (
              <li key={String(entry.id)}>{entry.title}</li>
            ))}
          </ul>
        </div>
      ))}
    </div>
  );
}
