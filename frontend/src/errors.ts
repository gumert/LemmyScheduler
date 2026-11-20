import { Code, ConnectError } from "@connectrpc/connect";

export type FieldErrors = Partial<Record<"title" | "body" | "url" | "community" | "scheduled_at" | "status", string>>;

const FIELDS = ["title", "body", "url", "community", "scheduled_at", "status"] as const;

// Maps a Connect error to a message for the user. InvalidArgument and
// FailedPrecondition carry the backend's own explanation, which is shown as is.
export function describeApiError(err: unknown): string {
  if (err instanceof ConnectError) {
    switch (err.code) {
      case Code.InvalidArgument:
      case Code.FailedPrecondition:
        return err.rawMessage;
      case Code.NotFound:
        return "This post no longer exists.";
      case Code.DeadlineExceeded:
      case Code.Canceled:
        return "The request timed out. Check that the backend is running and try again.";
      case Code.Unavailable:
        return "The backend is unreachable.";
      default:
        return `Request failed: ${err.rawMessage}`;
    }
  }
  return err instanceof Error ? err.message : String(err);
}

// The backend reports validation failures as "<field>: <message>". Returns the
// field when the message names one we render inline.
export function fieldErrorFor(err: unknown): { field: keyof FieldErrors; message: string } | undefined {
  if (!(err instanceof ConnectError) || err.code !== Code.InvalidArgument) {
    return undefined;
  }
  for (const field of FIELDS) {
    const prefix = `${field}: `;
    if (err.rawMessage.startsWith(prefix)) {
      return { field, message: err.rawMessage.slice(prefix.length) };
    }
  }
  return undefined;
}

// Maps an image endpoint's HTTP status to a message. Server limits are
// authoritative, so 413 and 415 are reported even if the pre-check passed.
export function describeImageStatus(status: number): string {
  switch (status) {
    case 404:
      return "The post or image no longer exists.";
    case 409:
      return "This post can't be changed right now (it may be posting or already posted), or the image is still in use.";
    case 413:
      return "The image is too large, or the post has reached its image limit.";
    case 415:
      return "That file isn't a supported image (JPEG, PNG, GIF or WebP).";
    default:
      return `Image request failed (HTTP ${status}).`;
  }
}
