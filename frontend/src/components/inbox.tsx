"use client";

import Link from "next/link";
import { useCallback, useEffect, useReducer, useRef, useState } from "react";

import { scheduledPostClient, titleImageUrl } from "../api/client";
import { EMPTY_SELECTION, groupSkips, selectionReducer } from "../bulk";
import { describeApiError } from "../errors";
import { BulkAction, ScheduledPostStatus, type ScheduledPost } from "../gen/lemmyscheduler/v1/scheduled_post_pb";
import { bodySnippet } from "../snippet";
import { HISTORY_STATUSES, statusClass, statusLabel, UPCOMING_STATUSES } from "../status";
import { formatScheduledAt, localZoneName } from "../time";
import { BulkResult, BulkToolbar, type BulkResultView } from "./bulk-toolbar";

const LIST_TIMEOUT_MS = 10_000;
const BULK_TIMEOUT_MS = 30_000;
const PAGE_SIZE = 20;

type Tab = "upcoming" | "history";

type ListState = {
  posts: ScheduledPost[];
  nextToken: string;
  phase: "loading" | "ready" | "error";
  loadingMore: boolean;
  error: string;
  errorKind: "more" | "refresh";
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
        <span className="inline">
          <Link href="/settings" className="button">
            Settings
          </Link>
          <Link href="/posts/new" className="button primary">
            New post
          </Link>
        </span>
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
    errorKind: "more",
  });
  const [selection, dispatch] = useReducer(selectionReducer, EMPTY_SELECTION);
  const [busy, setBusy] = useState(false);
  const busyRef = useRef(false);
  const [result, setResult] = useState<BulkResultView | null>(null);
  const [bulkError, setBulkError] = useState("");
  const selectAllRef = useRef<HTMLInputElement>(null);
  const controllerRef = useRef<AbortController | null>(null);

  const load = useCallback(
    async (pageToken: string, silent = false) => {
      controllerRef.current?.abort();
      const controller = new AbortController();
      controllerRef.current = controller;
      const more = pageToken !== "";
      // A silent refresh keeps the rows and any open bulk UI in place.
      setState((s) =>
        more
          ? { ...s, loadingMore: true, error: "" }
          : silent
            ? { ...s, error: "" }
            : { ...s, phase: "loading", error: "" },
      );
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
          errorKind: "more",
        }));
      } catch (err) {
        if (controller.signal.aborted) {
          return;
        }
        // A failed "load more" keeps the rows already shown.
        setState((s) => ({
          ...s,
          phase: more || silent ? "ready" : "error",
          loadingMore: false,
          error: describeApiError(err),
          errorKind: silent ? "refresh" : "more",
        }));
      }
    },
    [tab],
  );

  useEffect(() => {
    void load("");
    return () => controllerRef.current?.abort();
  }, [load]);

  // Rows that left the list (deleted, or gone after a refresh) can't stay selected.
  useEffect(() => {
    dispatch({ type: "retain", ids: state.posts.map((post) => post.id) });
  }, [state.posts]);

  const visibleIds = state.posts.map((post) => post.id);
  const selectedVisible = visibleIds.filter((id) => selection.has(id)).length;
  useEffect(() => {
    if (selectAllRef.current) {
      selectAllRef.current.indeterminate = selectedVisible > 0 && selectedVisible < visibleIds.length;
    }
  });

  async function runBulk(action: BulkAction, delaySeconds?: number) {
    if (busyRef.current || selection.size === 0) {
      return;
    }
    if (
      action === BulkAction.DELETE &&
      !window.confirm(`Delete ${selection.size} post${selection.size === 1 ? "" : "s"}? This can't be undone.`)
    ) {
      return;
    }
    busyRef.current = true;
    setBusy(true);
    setBulkError("");
    setResult(null);
    const titles = new Map(state.posts.map((post) => [post.id, post.title]));
    try {
      const res = await scheduledPostClient.bulkUpdateScheduledPosts(
        { ids: [...selection], action, delaySeconds: BigInt(delaySeconds ?? 0) },
        { timeoutMs: BULK_TIMEOUT_MS },
      );
      dispatch({ type: "setAll", ids: res.updatedIds, selected: false });
      setResult({
        updated: res.updatedIds.length,
        skipped: res.skipped.length,
        groups: groupSkips(res.skipped, titles),
      });
      await load("", true);
    } catch (err) {
      setBulkError(describeApiError(err));
    } finally {
      busyRef.current = false;
      setBusy(false);
    }
  }

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
      <div className="select-bar">
        <label className="select-all">
          <input
            ref={selectAllRef}
            type="checkbox"
            checked={selectedVisible > 0 && selectedVisible === visibleIds.length}
            disabled={busy}
            onChange={(event) => dispatch({ type: "setAll", ids: visibleIds, selected: event.target.checked })}
          />
          Select all loaded
        </label>
        <span className="muted">{selection.size} selected</span>
        <BulkToolbar count={selection.size} busy={busy} onRun={(a, s) => void runBulk(a, s)} />
      </div>
      {bulkError && (
        <p className="state state-error" role="alert">
          Bulk action failed: {bulkError}
        </p>
      )}
      {result && <BulkResult result={result} onDismiss={() => setResult(null)} />}
      <ul className="post-list">
        {state.posts.map((post) => (
          <PostRow
            key={String(post.id)}
            post={post}
            selected={selection.has(post.id)}
            disabled={busy}
            onToggle={() => dispatch({ type: "toggle", id: post.id })}
          />
        ))}
      </ul>
      {state.error && (
        <p className="state state-error" role="alert">
          {state.errorKind === "refresh" ? "Couldn't refresh" : "Couldn't load more"}: {state.error}
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

function PostRow({
  post,
  selected,
  disabled,
  onToggle,
}: {
  post: ScheduledPost;
  selected: boolean;
  disabled: boolean;
  onToggle: () => void;
}) {
  const snippet = bodySnippet(post.body);
  const when = post.status === ScheduledPostStatus.POSTED ? post.postedAt : post.scheduledAt;
  const label = statusLabel(post.status);
  return (
    <li className="post-item">
      <label className="post-select">
        <input
          type="checkbox"
          checked={selected}
          disabled={disabled}
          onChange={onToggle}
          aria-label={`Select ${post.title}`}
        />
      </label>
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
