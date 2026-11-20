// Matches the backend's token grammar (scheduledposts.BodyImageIDs).
export const IMAGE_TOKEN_PATTERN = /\[\[image:([1-9][0-9]{0,17})\]\]/;

export type BodySegment = { type: "text"; text: string } | { type: "image"; id: bigint };

export function imageToken(id: bigint): string {
  return `[[image:${id}]]`;
}

// Distinct referenced image IDs, in order of first appearance.
export function bodyImageIds(body: string): bigint[] {
  const seen = new Set<bigint>();
  const ids: bigint[] = [];
  for (const match of body.matchAll(new RegExp(IMAGE_TOKEN_PATTERN.source, "g"))) {
    const id = BigInt(match[1]);
    if (!seen.has(id)) {
      seen.add(id);
      ids.push(id);
    }
  }
  return ids;
}

// Inserts a token at the given caret (clamped), on its own line.
export function insertImageToken(body: string, id: bigint, caret: number): string {
  const at = Math.min(Math.max(caret, 0), body.length);
  const before = body.slice(0, at);
  const after = body.slice(at);
  const prefix = before === "" || before.endsWith("\n") ? "" : "\n";
  const suffix = after === "" || after.startsWith("\n") ? "" : "\n";
  return `${before}${prefix}${imageToken(id)}${suffix}${after}`;
}

// Removes every reference to the image. A line holding only the token is
// dropped; an inline token is removed in place.
export function removeImageToken(body: string, id: bigint): string {
  const token = imageToken(id);
  return body
    .split("\n")
    .filter((line) => line !== token)
    .map((line) => line.split(token).join(""))
    .join("\n");
}

// Splits a body into text and image segments for inline rendering.
export function splitBody(body: string): BodySegment[] {
  const segments: BodySegment[] = [];
  let last = 0;
  for (const match of body.matchAll(new RegExp(IMAGE_TOKEN_PATTERN.source, "g"))) {
    const index = match.index ?? 0;
    if (index > last) {
      segments.push({ type: "text", text: body.slice(last, index) });
    }
    segments.push({ type: "image", id: BigInt(match[1]) });
    last = index + match[0].length;
  }
  if (last < body.length) {
    segments.push({ type: "text", text: body.slice(last) });
  }
  return segments;
}

// Stored images that saving `body` would delete: neither referenced nor the
// title image (mirrors the backend's reconcile step).
export function orphanedImageIds(body: string, images: { id: bigint }[], titleId: bigint | undefined): bigint[] {
  const referenced = new Set(bodyImageIds(body));
  return images.map((image) => image.id).filter((id) => id !== titleId && !referenced.has(id));
}
