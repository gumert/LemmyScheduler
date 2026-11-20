// Mirrors the backend rule: an absolute http or https URL with a host.
export function isHttpUrl(value: string): boolean {
  try {
    const url = new URL(value);
    return (url.protocol === "http:" || url.protocol === "https:") && url.host !== "";
  } catch {
    return false;
  }
}

// Returns a message, or undefined when the value is empty or acceptable.
export function urlProblem(value: string): string | undefined {
  const trimmed = value.trim();
  if (trimmed === "") {
    return undefined;
  }
  if (!isHttpUrl(trimmed)) {
    return "Enter a full web address starting with http:// or https://.";
  }
  return trimmed === value ? undefined : "Remove the spaces around the URL.";
}
