// Limits mirror backend/internal/scheduledposts/images.go. The server is
// authoritative; these only give fast feedback.
export const MAX_IMAGE_BYTES = 10 * 1024 * 1024;
export const MAX_BODY_IMAGES = 10;
export const MAX_POST_IMAGES = 11;
export const ALLOWED_IMAGE_TYPES = ["image/jpeg", "image/png", "image/gif", "image/webp"] as const;

export const IMAGE_ACCEPT = ALLOWED_IMAGE_TYPES.join(",");

// Returns a user-facing message, or undefined when the file looks acceptable.
export function checkImageFile(file: { type: string; size: number }): string | undefined {
  if (!(ALLOWED_IMAGE_TYPES as readonly string[]).includes(file.type)) {
    return "Images must be JPEG, PNG, GIF or WebP.";
  }
  if (file.size === 0) {
    return "That image file is empty.";
  }
  if (file.size > MAX_IMAGE_BYTES) {
    return `Images must be ${MAX_IMAGE_BYTES / (1024 * 1024)} MiB or smaller.`;
  }
  return undefined;
}

// Checks whether one more image can be referenced from the body.
export function checkBodyImageCount(referencedCount: number, totalImageCount: number): string | undefined {
  if (referencedCount >= MAX_BODY_IMAGES) {
    return `A post body can include at most ${MAX_BODY_IMAGES} images.`;
  }
  if (totalImageCount >= MAX_POST_IMAGES) {
    return `A post can store at most ${MAX_POST_IMAGES} images.`;
  }
  return undefined;
}
