import { IMAGE_TOKEN_PATTERN } from "./body-images";

export const SNIPPET_MAX_LENGTH = 140;

// One-line preview of a post body: image tokens dropped, whitespace collapsed,
// truncated on a code point boundary.
export function bodySnippet(body: string, maxLength = SNIPPET_MAX_LENGTH): string {
  const text = body.replace(new RegExp(IMAGE_TOKEN_PATTERN.source, "g"), " ").replace(/\s+/g, " ").trim();
  const chars = Array.from(text);
  if (chars.length <= maxLength) {
    return text;
  }
  return `${chars
    .slice(0, maxLength - 1)
    .join("")
    .trimEnd()}…`;
}
