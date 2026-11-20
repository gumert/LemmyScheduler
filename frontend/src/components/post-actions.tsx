import { ScheduledPostStatus } from "../gen/lemmyscheduler/v1/scheduled_post_pb";
import { statusActions, type StatusAction } from "../status";

type Props = {
  status: ScheduledPostStatus;
  editable: boolean;
  busy: boolean;
  canSave: boolean;
  futureTime: boolean;
  canDelete: boolean;
  onSave: () => void;
  onAction: (action: StatusAction) => void;
  onDelete: () => void;
};

export function PostActions({
  status,
  editable,
  busy,
  canSave,
  futureTime,
  canDelete,
  onSave,
  onAction,
  onDelete,
}: Props) {
  return (
    <div className="actions">
      {editable && (
        <>
          <button type="button" className="button primary" disabled={!canSave} onClick={onSave}>
            {busy ? "Working…" : status === ScheduledPostStatus.DRAFT ? "Save draft" : "Save changes"}
          </button>
          {statusActions(status).map((action) => (
            <button
              key={action.label}
              type="button"
              className="button"
              disabled={!canSave || (action.schedules && !futureTime)}
              onClick={() => onAction(action)}
            >
              {action.label}
            </button>
          ))}
        </>
      )}
      {canDelete && (
        <button type="button" className="button danger" disabled={busy} onClick={onDelete}>
          Delete
        </button>
      )}
    </div>
  );
}
