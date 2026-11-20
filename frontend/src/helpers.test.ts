import { describe, expect, it } from "vitest";

import { timestampDate } from "@bufbuild/protobuf/wkt";

import { bodyImageIds, orphanedImageIds, insertImageToken, removeImageToken, splitBody } from "./body-images";
import { describeApiError, describeImageStatus, fieldErrorFor } from "./errors";
import { checkBodyImageCount, checkImageFile, MAX_IMAGE_BYTES } from "./images";
import { bodySnippet } from "./snippet";
import { urlProblem } from "./url";
import { defaultScheduledLocal, formatScheduledAt, localInputToTimestamp, timestampToLocalInput } from "./time";
import { Code, ConnectError } from "@connectrpc/connect";

// TZ is read when the first Date is created, so it is set before any test runs.
process.env.TZ = "America/New_York";

describe("datetime-local conversion", () => {
  it("round-trips a local time through UTC", () => {
    const ts = localInputToTimestamp("2026-07-01T09:30");
    expect(timestampDate(ts!).toISOString()).toBe("2026-07-01T13:30:00.000Z");
    expect(timestampToLocalInput(ts)).toBe("2026-07-01T09:30");
  });

  it("uses the zone's standard offset in winter", () => {
    const ts = localInputToTimestamp("2026-01-15T09:30");
    expect(timestampDate(ts!).toISOString()).toBe("2026-01-15T14:30:00.000Z");
  });

  it("handles the DST boundary", () => {
    // 2026-03-08 02:00 -> 03:00 in New York: 02:30 does not exist.
    expect(localInputToTimestamp("2026-03-08T02:30")).toBeUndefined();
    const before = localInputToTimestamp("2026-03-08T01:30")!;
    const after = localInputToTimestamp("2026-03-08T03:30")!;
    expect(timestampDate(after).getTime() - timestampDate(before).getTime()).toBe(3600_000);
    expect(timestampToLocalInput(after)).toBe("2026-03-08T03:30");
  });

  it("rejects empty and malformed input", () => {
    expect(localInputToTimestamp("")).toBeUndefined();
    expect(localInputToTimestamp("2026-13-01T00:00")).toBeUndefined();
    expect(timestampToLocalInput(undefined)).toBe("");
  });

  it("defaults to 24 elapsed hours from now, even across a DST change", () => {
    expect(defaultScheduledLocal(new Date("2026-07-01T13:30:00Z"))).toBe("2026-07-02T09:30");
    // 24 hours after 2026-03-07 12:00 EST is 13:00 EDT the next day.
    expect(defaultScheduledLocal(new Date("2026-03-07T17:00:00Z"))).toBe("2026-03-08T13:00");
  });

  it("formats rows with a zone name or as unscheduled", () => {
    expect(formatScheduledAt(undefined)).toBe("Unscheduled");
    expect(formatScheduledAt(localInputToTimestamp("2026-07-01T09:30"), "en-US")).toMatch(/EDT/);
  });
});

describe("bodySnippet", () => {
  it("collapses whitespace and drops image tokens", () => {
    expect(bodySnippet("  hello \n\n [[image:12]]  world\t!")).toBe("hello world !");
  });

  it("truncates with an ellipsis without splitting code points", () => {
    const snippet = bodySnippet("😀".repeat(10), 5);
    expect(Array.from(snippet)).toHaveLength(5);
    expect(snippet.endsWith("…")).toBe(true);
    expect(bodySnippet("short", 5)).toBe("short");
  });
});

describe("body image tokens", () => {
  it("lists distinct ids in order", () => {
    expect(bodyImageIds("[[image:3]] a [[image:1]] [[image:3]] [[image:0]]")).toEqual([BigInt(3), BigInt(1)]);
  });

  it("inserts on its own line and removes cleanly", () => {
    const inserted = insertImageToken("hello world", BigInt(7), 5);
    expect(inserted).toBe("hello\n[[image:7]]\n world");
    expect(removeImageToken(inserted, BigInt(7))).toBe("hello\n world");
    expect(removeImageToken("a [[image:7]] b", BigInt(7))).toBe("a  b");
    expect(removeImageToken("[[image:7]]\nbody", BigInt(7))).toBe("body");
    expect(removeImageToken("body\n[[image:7]]", BigInt(7))).toBe("body");
  });

  it("splits a body into text and image segments", () => {
    expect(splitBody("a[[image:5]]b")).toEqual([
      { type: "text", text: "a" },
      { type: "image", id: BigInt(5) },
      { type: "text", text: "b" },
    ]);
  });
});

describe("error messages", () => {
  it("shows backend messages for InvalidArgument and FailedPrecondition", () => {
    expect(describeApiError(new ConnectError("title: is required", Code.InvalidArgument))).toBe("title: is required");
    expect(describeApiError(new ConnectError("post is not editable", Code.FailedPrecondition))).toBe(
      "post is not editable",
    );
    expect(describeApiError(new ConnectError("gone", Code.NotFound))).toMatch(/no longer exists/);
  });

  it("extracts the field from a validation message", () => {
    expect(fieldErrorFor(new ConnectError("body: references unknown image 4", Code.InvalidArgument))).toEqual({
      field: "body",
      message: "references unknown image 4",
    });
    expect(fieldErrorFor(new ConnectError("nope", Code.InvalidArgument))).toBeUndefined();
  });

  it("maps image upload statuses", () => {
    expect(describeImageStatus(404)).toMatch(/no longer exists/);
    expect(describeImageStatus(409)).toMatch(/can't be changed/);
    expect(describeImageStatus(413)).toMatch(/too large/);
    expect(describeImageStatus(415)).toMatch(/supported image/);
    expect(describeImageStatus(500)).toMatch(/500/);
  });
});

describe("image pre-checks", () => {
  it("checks type and size", () => {
    expect(checkImageFile({ type: "image/png", size: 10 })).toBeUndefined();
    expect(checkImageFile({ type: "image/svg+xml", size: 10 })).toMatch(/JPEG/);
    expect(checkImageFile({ type: "image/png", size: MAX_IMAGE_BYTES + 1 })).toMatch(/MiB/);
    expect(checkImageFile({ type: "image/png", size: MAX_IMAGE_BYTES })).toBeUndefined();
  });

  it("checks body and total image counts", () => {
    expect(checkBodyImageCount(9, 10)).toBeUndefined();
    expect(checkBodyImageCount(10, 10)).toMatch(/at most 10/);
    expect(checkBodyImageCount(3, 11)).toMatch(/at most 11/);
  });
});

describe("urlProblem", () => {
  it("accepts empty and absolute http(s) URLs", () => {
    expect(urlProblem("")).toBeUndefined();
    expect(urlProblem("https://example.com/a?b=1")).toBeUndefined();
  });

  it("rejects other schemes, relative and malformed values", () => {
    expect(urlProblem("ftp://example.com")).toMatch(/http/);
    expect(urlProblem("example.com")).toMatch(/http/);
    expect(urlProblem("https://")).toMatch(/http/);
    expect(urlProblem(" https://example.com")).toMatch(/spaces/);
  });
});

describe("orphanedImageIds", () => {
  it("finds stored images a save would delete", () => {
    const images = [{ id: BigInt(1) }, { id: BigInt(2) }, { id: BigInt(3) }];
    expect(orphanedImageIds("a [[image:1]]", images, BigInt(3))).toEqual([BigInt(2)]);
    expect(orphanedImageIds("[[image:1]][[image:2]]", images, undefined)).toEqual([BigInt(3)]);
  });
});
