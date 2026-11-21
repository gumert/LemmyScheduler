import { createClient } from "@connectrpc/connect";
import { createConnectTransport } from "@connectrpc/connect-web";

import { describeImageStatus } from "../errors";
import { LemmyAccountService } from "../gen/lemmyscheduler/v1/lemmy_account_pb";
import { PingService } from "../gen/lemmyscheduler/v1/ping_pb";
import { ScheduledPostService } from "../gen/lemmyscheduler/v1/scheduled_post_pb";

// NEXT_PUBLIC_* values are inlined at build time, so a production build needs
// this set when `next build` runs. The default matches the local backend.
const API_URL = process.env.NEXT_PUBLIC_API_URL ?? "http://localhost:8080";

// This is the only module that builds API URLs. Components import the typed
// clients below and never construct URLs themselves.
const transport = createConnectTransport({ baseUrl: API_URL });

export const pingClient = createClient(PingService, transport);
export const scheduledPostClient = createClient(ScheduledPostService, transport);
export const lemmyAccountClient = createClient(LemmyAccountService, transport);

export class ImageRequestError extends Error {
  constructor(readonly status: number) {
    super(describeImageStatus(status));
  }
}

// Image bytes are served over plain HTTP next to the Connect routes. The server
// revalidates with ETags, and `v` (the current image id) busts the browser's
// cache when a title image is replaced.
function imagePath(postId: bigint, imageId?: bigint): string {
  return imageId === undefined
    ? `/scheduled-posts/${postId}/title-image`
    : `/scheduled-posts/${postId}/images/${imageId}`;
}

export function titleImageUrl(postId: bigint, imageId: bigint): string {
  return `${API_URL}${imagePath(postId)}?v=${imageId}`;
}

export function bodyImageUrl(postId: bigint, imageId: bigint): string {
  return `${API_URL}${imagePath(postId, imageId)}?v=${imageId}`;
}

const IMAGE_TIMEOUT_MS = 60_000;

async function imageFetch(path: string, init: RequestInit): Promise<Response> {
  const res = await fetch(`${API_URL}${path}`, { ...init, signal: AbortSignal.timeout(IMAGE_TIMEOUT_MS) });
  if (!res.ok) {
    throw new ImageRequestError(res.status);
  }
  return res;
}

async function upload(method: "PUT" | "POST", path: string, file: File): Promise<bigint> {
  const res = await imageFetch(path, { method, body: file, headers: { "Content-Type": file.type } });
  const json = (await res.json()) as { id: string };
  return BigInt(json.id);
}

// Replaces the title image with an uploaded file and returns its new id.
export function putTitleImage(postId: bigint, file: File): Promise<bigint> {
  return upload("PUT", imagePath(postId), file);
}

// Uploads a body image and returns its id; the caller must reference it from
// the body and save, or the next save deletes it.
export function createBodyImage(postId: bigint, file: File): Promise<bigint> {
  return upload("POST", `/scheduled-posts/${postId}/images`, file);
}

export async function selectTitleImage(postId: bigint, imageId: bigint): Promise<void> {
  await imageFetch(`${imagePath(postId)}/${imageId}`, { method: "PUT" });
}

export async function deleteTitleImage(postId: bigint): Promise<void> {
  await imageFetch(imagePath(postId), { method: "DELETE" });
}

export async function deleteBodyImage(postId: bigint, imageId: bigint): Promise<void> {
  await imageFetch(imagePath(postId, imageId), { method: "DELETE" });
}
