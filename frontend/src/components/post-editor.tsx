"use client";

import dynamic from "next/dynamic";
import Link from "next/link";
import { useEffect, useState } from "react";

import { scheduledPostClient } from "../api/client";
import { describeApiError } from "../errors";
import type { ScheduledPost } from "../gen/lemmyscheduler/v1/scheduled_post_pb";
import { PostForm } from "./post-form";

// The default scheduled time depends on the browser's clock and zone, so the
// new-post form is never rendered on the server.
const NewPostForm = dynamic(() => import("./post-form").then((module) => module.PostForm), { ssr: false });

export function NewPostEditor() {
  return <NewPostForm />;
}

const LOAD_TIMEOUT_MS = 10_000;

type LoadState = { phase: "loading" } | { phase: "error"; error: string } | { phase: "ready"; post: ScheduledPost };

function parseId(value: string): bigint | undefined {
  return /^[1-9][0-9]{0,17}$/.test(value) ? BigInt(value) : undefined;
}

export function PostEditor({ id }: { id: string }) {
  const postId = parseId(id);
  const [state, setState] = useState<LoadState>(
    postId === undefined ? { phase: "error", error: "That isn't a valid post id." } : { phase: "loading" },
  );

  useEffect(() => {
    if (postId === undefined) {
      return;
    }
    const controller = new AbortController();
    scheduledPostClient
      .getScheduledPost({ id: postId }, { timeoutMs: LOAD_TIMEOUT_MS, signal: controller.signal })
      .then((res) => {
        if (res.scheduledPost) {
          setState({ phase: "ready", post: res.scheduledPost });
        } else {
          setState({ phase: "error", error: "The backend returned no post." });
        }
      })
      .catch((err: unknown) => {
        if (!controller.signal.aborted) {
          setState({ phase: "error", error: describeApiError(err) });
        }
      });
    return () => controller.abort();
  }, [postId]);

  switch (state.phase) {
    case "loading":
      return (
        <p className="state state-loading" role="status">
          Loading post…
        </p>
      );
    case "error":
      return (
        <div className="state state-error" role="alert">
          <p>Couldn&apos;t load this post: {state.error}</p>
          <Link href="/">Back to inbox</Link>
        </div>
      );
    case "ready":
      return <PostForm initial={state.post} />;
  }
}
