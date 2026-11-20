"use client";

import Link from "next/link";
import { useCallback, useEffect, useRef, useState } from "react";

import { scheduledPostClient, titleImageUrl } from "../api/client";
import { describeApiError } from "../errors";
import { ScheduledPostStatus, type ScheduledPost } from "../gen/lemmyscheduler/v1/scheduled_post_pb";
import { bodySnippet } from "../snippet";
import { HISTORY_STATUSES, statusClass, statusLabel, UPCOMING_STATUSES } from "../status";
import { formatScheduledAt, localZoneName } from "../time";

const LIST_TIMEOUT_MS = 10_000;
const PAGE_SIZE = 20;

type Tab = "upcoming" | "history";

type ListState = {
  posts: ScheduledPost[];
  nextToken: string;
  phase: "loading" | "ready" | "error";
  loadingMore: boolean;
  error: string;
};

export function Inbox() {
  const [tab, setTab] = useState<Tab>("upcoming");

  return (
    <section>
      <div className="inbox-toolbar">
        <div role="tablist" className="tabs">
          <TabButton current={tab} value="upcoming" onSelect={setTab}>
            Upcoming
          </TabButton>
          <TabButton current={tab} value="history" onSelect={setTab}>
            History
          </TabButton>
        </div>
        <Link href="/posts/new" className="button primary">
          New post
        </Link>
      </div>
      <p className="muted">Times are shown in {localZoneName()}.</p>
      <PostList key={tab} tab={tab} />
    </section>
  );
}

function TabButton({
  current,
  value,
  onSelect,
  children,
}: {
  current: Tab;
  value: Tab;
  onSelect: (tab: Tab) => void;
  children: string;
}) {
  return (
    <button
      type="button"
      role="tab"
      aria-selected={current === value}
      className={current === value ? "tab active" : "tab"}
      onClick={() => onSelect(value)}
    >
      {children}
    </button>
  );
}

function PostList({ tab }: { tab: Tab }) {
  const [state, setState] = useState<ListState>({
    posts: [],
    nextToken: "",
    phase: "loading",
    loadingMore: false,
    error: "",
  });
  const controllerRef = useRef<AbortController | null>(null);

  const load = useCallback(
    async (pageToken: string) => {
      controllerRef.current?.abort();
      const controller = new AbortController();
      controllerRef.current = controller;
      const more = pageToken !== "";
      setState((s) => (more ? { ...s, loadingMore: true, error: "" } : { ...s, phase: "loading", error: "" }));
      try {
        const res = await scheduledPostClient.listScheduledPosts(
          {
            statuses: tab === "upcoming" ? UPCOMING_STATUSES : HISTORY_STATUSES,
            descending: tab === "history",
            pageSize: PAGE_SIZE,
            pageToken,
          },
          { timeoutMs: LIST_TIMEOUT_MS, signal: controller.signal },
        );
        setState((s) => ({
          posts: more ? [...s.posts, ...res.scheduledPosts] : res.scheduledPosts,
          nextToken: res.nextPageToken,
          phase: "ready",
          loadingMore: false,
          error: "",
        }));
      } catch (err) {
        if (controller.signal.aborted) {
          return;
        }
        // A failed "load more" keeps the rows already shown.
        setState((s) => ({
          ...s,
          phase: more ? "ready" : "error",
          loadingMore: false,
          error: describeApiError(err),
        }));
      }
    },
    [tab],
  );

  useEffect(() => {
    void load("");
    return () => controllerRef.current?.abort();
  }, [load]);

  if (state.phase === "loading") {
    return (
      <p className="state state-loading" role="status">
        Loading posts…
      </p>
    );
  }
  if (state.phase === "error") {
    return (
      <div className="state state-error" role="alert">
        <p>Couldn&apos;t load posts: {state.error}</p>
        <button type="button" className="button" onClick={() => void load("")}>
          Retry
        </button>
      </div>
    );
  }
  if (state.posts.length === 0) {
    return (
      <p className="state state-empty">
        {tab === "upcoming" ? "No upcoming posts. Create one to get started." : "Nothing has been posted yet."}
      </p>
    );
  }

  return (
    <>
      <ul className="post-list">
        {state.posts.map((post) => (
          <PostRow key={String(post.id)} post={post} />
        ))}
      </ul>
      {state.error && (
        <p className="state state-error" role="alert">
          Couldn&apos;t load more: {state.error}
        </p>
      )}
      {state.nextToken && (
        <button
          type="button"
          className="button load-more"
          disabled={state.loadingMore}
          onClick={() => void load(state.nextToken)}
        >
          {state.loadingMore ? "Loading…" : "Load more"}
        </button>
      )}
    </>
  );
}

function PostRow({ post }: { post: ScheduledPost }) {
  const snippet = bodySnippet(post.body);
  const when = post.status === ScheduledPostStatus.POSTED ? post.postedAt : post.scheduledAt;
  const label = statusLabel(post.status);
  return (
    <li>
      {/* Handoff 06 adds a checkbox cell before the link. */}
      <Link href={`/posts/${post.id}`} className="post-row">
        <span className="thumb">
          {post.titleImage && (
            // eslint-disable-next-line @next/next/no-img-element -- bytes come from the API, not next/image
            <img src={titleImageUrl(post.id, post.titleImage.id)} alt="" loading="lazy" />
          )}
        </span>
        <span className="post-main">
          <span className="post-heading">
            <span className={`badge badge-${statusClass(post.status)}`}>{label}</span>
            <span className="post-title">{post.title}</span>
          </span>
          <span className="post-meta">
            {post.community} · {formatScheduledAt(when)}
          </span>
          {snippet && <span className="post-snippet">{snippet}</span>}
        </span>
      </Link>
    </li>
  );
}
