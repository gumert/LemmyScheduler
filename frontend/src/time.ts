import { timestampDate, timestampFromDate, type Timestamp } from "@bufbuild/protobuf/wkt";

const LOCAL_PATTERN = /^(\d{4})-(\d{2})-(\d{2})T(\d{2}):(\d{2})$/;

export function localZoneName(): string {
  return Intl.DateTimeFormat().resolvedOptions().timeZone;
}

function pad(value: number, width = 2): string {
  return String(value).padStart(width, "0");
}

// Formats an instant for <input type="datetime-local"> in the runtime's zone.
export function timestampToLocalInput(timestamp: Timestamp | undefined): string {
  if (!timestamp) {
    return "";
  }
  const date = timestampDate(timestamp);
  return (
    `${pad(date.getFullYear(), 4)}-${pad(date.getMonth() + 1)}-${pad(date.getDate())}` +
    `T${pad(date.getHours())}:${pad(date.getMinutes())}`
  );
}

// Parses a datetime-local value as wall-clock time in the runtime's zone.
// Returns undefined for empty or malformed values, and for wall-clock times
// that do not exist (the spring-forward gap).
export function localInputToTimestamp(value: string): Timestamp | undefined {
  const match = LOCAL_PATTERN.exec(value);
  if (!match) {
    return undefined;
  }
  const [year, month, day, hour, minute] = match.slice(1).map(Number);
  const date = new Date(year, month - 1, day, hour, minute);
  if (
    date.getFullYear() !== year ||
    date.getMonth() !== month - 1 ||
    date.getDate() !== day ||
    date.getHours() !== hour ||
    date.getMinutes() !== minute
  ) {
    return undefined;
  }
  return timestampFromDate(date);
}

// The suggested time for a new post: 24 hours from now, as a datetime-local value.
export function defaultScheduledLocal(now: Date = new Date()): string {
  return timestampToLocalInput(timestampFromDate(new Date(now.getTime() + 24 * 60 * 60 * 1000)));
}

export function isFuture(timestamp: Timestamp | undefined, now: Date = new Date()): boolean {
  return timestamp !== undefined && timestampDate(timestamp).getTime() > now.getTime();
}

// Row display: local date and time with the zone name, or "Unscheduled".
export function formatScheduledAt(timestamp: Timestamp | undefined, locale?: string): string {
  if (!timestamp) {
    return "Unscheduled";
  }
  return formatInstant(timestampDate(timestamp), locale);
}

export function formatInstant(date: Date, locale?: string): string {
  return new Intl.DateTimeFormat(locale, {
    year: "numeric",
    month: "short",
    day: "numeric",
    hour: "numeric",
    minute: "2-digit",
    timeZoneName: "short",
  }).format(date);
}
