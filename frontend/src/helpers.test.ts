import { describe, expect, it } from "vitest";

import { timestampDate } from "@bufbuild/protobuf/wkt";

import { bodyImageIds, orphanedImageIds, insertImageToken, removeImageToken, splitBody } from "./body-images";
import { describeAccountError, describeApiError, describeImageStatus, fieldErrorFor, isNoAccountError } from "./errors";
import { checkBodyImageCount, checkImageFile, MAX_IMAGE_BYTES } from "./images";
import { create } from "@bufbuild/protobuf";

import { customDelaySeconds, EMPTY_SELECTION, groupSkips, selectionReducer, summarizeBulk } from "./bulk";
import { BulkSkippedPostSchema, BulkSkipReason } from "./gen/lemmyscheduler/v1/scheduled_post_pb";
import { bodySnippet } from "./snippet";
import { urlProblem } from "./url";
import { defaultScheduledLocal, formatScheduledAt, localInputToTimestamp, timestampToLocalInput } from "./time";
import { communitySuggestionLabel } from "./use-community-suggestions";
import { Code, ConnectError } from "@connectrpc/connect";

describe("community suggestions", () => {
  it("distinguishes communities with the same title on different instances", () => {
    expect(communitySuggestionLabel({ name: "cats@one.example", title: "Cats" })).toBe("cats@one.example — Cats");
    expect(communitySuggestionLabel({ name: "cats@two.example", title: "Cats" })).toBe("cats@two.example — Cats");
    expect(communitySuggestionLabel({ name: "cats@one.example", title: "" })).toBe("cats@one.example");
  });
});

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

describe("bulk selection", () => {
  it("toggles, selects a page, retains and clears", () => {
    let state = selectionReducer(EMPTY_SELECTION, { type: "toggle", id: BigInt(1) });
    state = selectionReducer(state, { type: "setAll", ids: [BigInt(1), BigInt(2), BigInt(3)], selected: true });
    expect([...state]).toEqual([BigInt(1), BigInt(2), BigInt(3)]);
    state = selectionReducer(state, { type: "toggle", id: BigInt(2) });
    expect([...state]).toEqual([BigInt(1), BigInt(3)]);
    state = selectionReducer(state, { type: "retain", ids: [BigInt(3), BigInt(4)] });
    expect([...state]).toEqual([BigInt(3)]);
    expect(selectionReducer(state, { type: "retain", ids: [BigInt(3)] })).toBe(state);
    expect(selectionReducer(state, { type: "clear" }).size).toBe(0);
  });

  it("deselects only the given ids", () => {
    const state = selectionReducer(new Set([BigInt(1), BigInt(2), BigInt(9)]), {
      type: "setAll",
      ids: [BigInt(1), BigInt(2)],
      selected: false,
    });
    expect([...state]).toEqual([BigInt(9)]);
  });
});

describe("bulk delay", () => {
  it("converts custom amounts to seconds", () => {
    expect(customDelaySeconds("36", "hours")).toBe(129_600);
    expect(customDelaySeconds("2", "days")).toBe(172_800);
  });

  it("rejects empty, zero, fractional, negative and oversized amounts", () => {
    for (const amount of ["", "0", "1.5", "-1", "abc", "366"]) {
      expect(customDelaySeconds(amount, "days")).toBeUndefined();
    }
    expect(customDelaySeconds("365", "days")).toBe(365 * 86_400);
    expect(customDelaySeconds("8761", "hours")).toBeUndefined();
  });
});

describe("bulk results", () => {
  it("groups skips by reason with titles", () => {
    const skip = (id: bigint, reason: BulkSkipReason) => create(BulkSkippedPostSchema, { id, reason });
    const groups = groupSkips(
      [
        skip(BigInt(1), BulkSkipReason.OVERDUE),
        skip(BigInt(2), BulkSkipReason.WRONG_STATUS),
        skip(BigInt(3), BulkSkipReason.OVERDUE),
      ],
      new Map([
        [BigInt(1), "One"],
        [BigInt(3), "Three"],
      ]),
    );
    expect(groups.map((g) => g.reason)).toEqual([BulkSkipReason.OVERDUE, BulkSkipReason.WRONG_STATUS]);
    expect(groups[0].entries).toEqual([
      { id: BigInt(1), title: "One" },
      { id: BigInt(3), title: "Three" },
    ]);
    expect(groups[1].entries).toEqual([{ id: BigInt(2), title: "Post 2" }]);
    expect(summarizeBulk(3, 2)).toBe("3 updated, 2 skipped");
  });
});

describe("account errors", () => {
  it("treats only a missing account as worth a hint", () => {
    expect(isNoAccountError(new ConnectError("none", Code.NotFound))).toBe(true);
    expect(isNoAccountError(new ConnectError("down", Code.Unavailable))).toBe(false);
    expect(isNoAccountError(new Error("network"))).toBe(false);
  });

  it("shows the backend's message and local text for transport failures", () => {
    expect(
      describeAccountError(new ConnectError("This instance needs Lemmy 0.19 or newer.", Code.FailedPrecondition)),
    ).toBe("This instance needs Lemmy 0.19 or newer.");
    expect(describeAccountError(new ConnectError("The instance is unreachable.", Code.Unavailable))).toBe(
      "The instance is unreachable.",
    );
    expect(describeAccountError(new ConnectError("fetch failed", Code.Unavailable))).toBe(
      "The backend is unreachable.",
    );
  });
});
