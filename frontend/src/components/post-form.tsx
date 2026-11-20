"use client";

import Link from "next/link";
import { useRouter } from "next/navigation";
import { useEffect, useRef, useState } from "react";

import {
  createBodyImage,
  deleteTitleImage,
  ImageRequestError,
  putTitleImage,
  scheduledPostClient,
  selectTitleImage,
  titleImageUrl,
} from "../api/client";
import { bodyImageIds, insertImageToken, orphanedImageIds, removeImageToken } from "../body-images";
import { describeApiError, fieldErrorFor, type FieldErrors } from "../errors";
import { ScheduledPostStatus, type ScheduledPost } from "../gen/lemmyscheduler/v1/scheduled_post_pb";
import { checkBodyImageCount, checkImageFile } from "../images";
import { isEditable, readOnlyReason, statusActions, statusClass, statusLabel, type StatusAction } from "../status";
import {
  defaultScheduledLocal,
  formatScheduledAt,
  isFuture,
  localInputToTimestamp,
  localZoneName,
  timestampToLocalInput,
} from "../time";
import { isHttpUrl, urlProblem } from "../url";
import { usePendingImage } from "../use-pending-image";
import { BodyImageList } from "./body-image-list";
import { BodyPreview } from "./body-preview";
import { Field } from "./field";
import { PostActions } from "./post-actions";
import { TitleImagePanel } from "./title-image-panel";

const REQUEST_TIMEOUT_MS = 15_000;
const UNSAVED_MESSAGE = "You have unsaved changes. Leave this page?";
const MISSING_LOCAL_TIME = "That local time doesn't exist (daylight-saving gap). Pick another time.";

type Fields = {
  title: string;
  body: string;
  url: string;
  community: string;
  nsfw: boolean;
  scheduledLocal: string;
};

function fieldsFrom(post: ScheduledPost | undefined): Fields {
  return {
    title: post?.title ?? "",
    body: post?.body ?? "",
    url: post?.url ?? "",
    community: post?.community ?? "",
    nsfw: post?.nsfw ?? false,
    scheduledLocal: post ? timestampToLocalInput(post.scheduledAt) : defaultScheduledLocal(),
  };
}

function sameFields(a: Fields, b: Fields): boolean {
  return (Object.keys(a) as (keyof Fields)[]).every((key) => a[key] === b[key]);
}

// One form for creating and editing. Everything that talks to the backend goes
// through run(), which allows a single operation at a time: a body save must
// never overlap an image upload, because saving deletes images the saved body
// does not reference.
//
// Handlers read `fields` and `post` from the render that created them. That is
// current because run() serializes operations and the fieldset is disabled
// while one is in flight, so nothing can edit the form in between.
export function PostForm({ initial }: { initial?: ScheduledPost }) {
  const router = useRouter();
  const [post, setPost] = useState<ScheduledPost | undefined>(initial);
  // Computed once so the baseline and the fields share the same default time.
  const [initialFields] = useState(() => fieldsFrom(initial));
  const [fields, setFields] = useState<Fields>(initialFields);
  // The fields as last saved. The form is dirty while they differ from this.
  const [baseline, setBaseline] = useState<Fields>(initialFields);
  const [busy, setBusy] = useState(false);
  const [formError, setFormError] = useState("");
  const [fieldErrors, setFieldErrors] = useState<FieldErrors>({});
  const [imageProblem, setImageProblem] = useState("");
  const { pending: pendingTitle, choose: choosePendingTitle, clear: clearPendingTitle } = usePendingImage();
  const busyRef = useRef(false);
  const bodyRef = useRef<HTMLTextAreaElement>(null);

  const status = post?.status ?? ScheduledPostStatus.DRAFT;
  const editable = post === undefined || isEditable(status);
  // The baseline moves as soon as the post is saved, but a title image chosen
  // on a new post uploads after that save. If the upload fails the post is
  // stored while the file is still pending, so the form stays dirty (and the
  // leave warning stays on) until the upload succeeds or the file is removed.
  const dirty = !sameFields(fields, baseline) || pendingTitle !== null;
  const scheduledAt = localInputToTimestamp(fields.scheduledLocal);
  const timeInvalid = fields.scheduledLocal !== "" && scheduledAt === undefined;
  const futureTime = isFuture(scheduledAt);
  const zone = localZoneName();
  const urlError = urlProblem(fields.url);
  const titleMissing = fields.title.trim() === "";

  useEffect(() => {
    if (!dirty && !busy) {
      return;
    }
    const handler = (event: BeforeUnloadEvent) => event.preventDefault();
    window.addEventListener("beforeunload", handler);
    return () => window.removeEventListener("beforeunload", handler);
  }, [dirty, busy]);

  function report(err: unknown) {
    if (err instanceof ImageRequestError) {
      setImageProblem(err.message);
      return;
    }
    const field = fieldErrorFor(err);
    if (field) {
      setFieldErrors({ [field.field]: field.message });
      return;
    }
    setFormError(describeApiError(err));
  }

  async function run(operation: () => Promise<void>) {
    if (busyRef.current) {
      return;
    }
    busyRef.current = true;
    setBusy(true);
    setFormError("");
    setFieldErrors({});
    setImageProblem("");
    try {
      await operation();
    } catch (err) {
      report(err);
    } finally {
      busyRef.current = false;
      setBusy(false);
    }
  }

  function update<K extends keyof Fields>(key: K, value: Fields[K]) {
    setFields((current) => ({ ...current, [key]: value }));
  }

  async function refresh(id: bigint) {
    const res = await scheduledPostClient.getScheduledPost({ id }, { timeoutMs: REQUEST_TIMEOUT_MS });
    if (res.scheduledPost) {
      setPost(res.scheduledPost);
    }
  }

  // Runs an image change on a stored post, then reloads the post to show it.
  function changeImages(id: bigint, change: () => Promise<unknown>) {
    void run(async () => {
      await change();
      await refresh(id);
    });
  }

  // Creates or updates the post with `next` and the given status, and returns
  // the stored post. Throws on failure.
  async function persist(next: Fields, nextStatus: ScheduledPostStatus): Promise<ScheduledPost> {
    const when = localInputToTimestamp(next.scheduledLocal);
    if (next.scheduledLocal !== "" && !when) {
      throw new Error(MISSING_LOCAL_TIME);
    }
    const scheduledPost = {
      title: next.title,
      body: next.body,
      url: next.url,
      community: next.community,
      nsfw: next.nsfw,
      status: nextStatus,
      scheduledAt: when,
    };
    const options = { timeoutMs: REQUEST_TIMEOUT_MS };
    const res = post
      ? await scheduledPostClient.updateScheduledPost({ id: post.id, scheduledPost }, options)
      : await scheduledPostClient.createScheduledPost({ scheduledPost }, options);
    const saved = res.scheduledPost;
    if (!saved) {
      throw new Error("The backend returned no post.");
    }
    setPost(saved);
    setBaseline(next);
    if (!post) {
      // Keeps this component (and any pending upload) mounted while the URL
      // becomes the post's own.
      window.history.replaceState(null, "", `/posts/${saved.id}`);
    }
    return saved;
  }

  // Returns false, with a message shown, when the upload fails; the file stays
  // pending so saving again retries it.
  async function uploadPendingTitle(saved: ScheduledPost, file: File): Promise<boolean> {
    try {
      await putTitleImage(saved.id, file);
    } catch (err) {
      const reason = err instanceof Error ? err.message : String(err);
      setImageProblem(`The post was saved, but its title image wasn't uploaded. ${reason}`);
      return false;
    }
    clearPendingTitle();
    await refresh(saved.id);
    return true;
  }

  // Saving deletes stored images the body no longer references, so ask first
  // when that would happen (for example a token that was cut and not yet pasted).
  function confirmOrphans(body: string, removing?: bigint): boolean {
    const orphans = orphanedImageIds(body, post?.images ?? [], post?.titleImage?.id).filter((id) => id !== removing);
    if (orphans.length === 0) {
      return true;
    }
    return window.confirm(
      `Saving will permanently delete ${orphans.length} image${orphans.length === 1 ? "" : "s"} that the body no longer references. Continue?`,
    );
  }

  // Saves the form. With a status action, also moves the post to that status.
  function save(action?: StatusAction) {
    if (!confirmOrphans(fields.body)) {
      return;
    }
    void run(async () => {
      const saved = await persist(fields, action?.to ?? status);
      const uploaded = pendingTitle ? await uploadPendingTitle(saved, pendingTitle.file) : true;
      // Scheduling is the end of the workflow, so return to the inbox.
      if (action?.schedules && uploaded) {
        router.push("/");
      }
    });
  }

  function remove() {
    if (!post || !window.confirm("Delete this post? This can't be undone.")) {
      return;
    }
    void run(async () => {
      await scheduledPostClient.deleteScheduledPost({ id: post.id }, { timeoutMs: REQUEST_TIMEOUT_MS });
      setBaseline(fields);
      router.push("/");
    });
  }

  function chooseTitleFile(file: File) {
    const problem = checkImageFile(file);
    if (problem) {
      setImageProblem(problem);
      return;
    }
    setImageProblem("");
    if (post) {
      changeImages(post.id, () => putTitleImage(post.id, file));
    } else {
      choosePendingTitle(file);
    }
  }

  function removeTitle() {
    if (pendingTitle) {
      clearPendingTitle();
    } else if (post) {
      changeImages(post.id, () => deleteTitleImage(post.id));
    }
  }

  function useExistingTitle(imageId: bigint) {
    if (post) {
      changeImages(post.id, () => selectTitleImage(post.id, imageId));
    }
  }

  // The token goes into the in-memory body the moment the upload returns, and
  // the save happens in the same operation, so no other save or edit can
  // delete the new image in between.
  function addBodyImage(file: File) {
    if (!post) {
      return;
    }
    const problem = checkImageFile(file) ?? checkBodyImageCount(bodyImageIds(fields.body).length, post.images.length);
    if (problem) {
      setImageProblem(problem);
      return;
    }
    if (!confirmOrphans(fields.body)) {
      return;
    }
    const caret = bodyRef.current?.selectionStart ?? fields.body.length;
    void run(async () => {
      const imageId = await createBodyImage(post.id, file);
      const next = { ...fields, body: insertImageToken(fields.body, imageId, caret) };
      setFields(next);
      try {
        await persist(next, status);
      } catch (err) {
        // The image is stored even though the save failed; show it.
        await refresh(post.id).catch(() => undefined);
        throw err;
      }
    });
  }

  function insertExisting(imageId: bigint) {
    const caret = bodyRef.current?.selectionStart ?? fields.body.length;
    update("body", insertImageToken(fields.body, imageId, caret));
  }

  function removeBodyImage(imageId: bigint) {
    if (!post) {
      return;
    }
    const body = removeImageToken(fields.body, imageId);
    if (!confirmOrphans(body, imageId)) {
      return;
    }
    void run(async () => {
      const next = { ...fields, body };
      setFields(next);
      await persist(next, status);
    });
  }

  function guardLeave(event: React.MouseEvent) {
    if (busy || (dirty && !window.confirm(UNSAVED_MESSAGE))) {
      event.preventDefault();
    }
  }

  const storedImages = post?.images ?? [];
  const storedIds = new Set(storedImages.map((image) => image.id));
  const titleId = post?.titleImage?.id;
  // Stored images missing from the body (for example a cut token) stay visible
  // until the next save deletes them.
  const detachedIds = orphanedImageIds(fields.body, storedImages, titleId);
  const listedIds = [...bodyImageIds(fields.body), ...detachedIds];
  const titlePreview = pendingTitle
    ? pendingTitle.previewUrl
    : post && titleId !== undefined
      ? titleImageUrl(post.id, titleId)
      : undefined;

  const hasTitleImage = titlePreview !== undefined;
  const hasUrl = fields.url.trim() !== "";
  const canSave = !busy && editable && !titleMissing && !timeInvalid && !urlError;
  const needsFutureTime = statusActions(status).some((action) => action.schedules);
  let hint = "";
  if (editable && !busy) {
    if (titleMissing) {
      hint = "A title is required to save this post.";
    } else if (timeInvalid) {
      hint = MISSING_LOCAL_TIME;
    } else if (needsFutureTime && !futureTime) {
      hint =
        fields.scheduledLocal === ""
          ? "Choose a scheduled time to schedule this post."
          : "The scheduled time must be in the future.";
    }
  }

  return (
    <form className="post-form" onSubmit={(event) => event.preventDefault()}>
      <Link href="/" onClick={guardLeave}>
        ← Inbox
      </Link>
      <h1>{post ? "Edit post" : "New post"}</h1>

      {post && (
        <p className="post-status">
          <span className={`badge badge-${statusClass(status)}`}>{statusLabel(status)}</span>
          {post.postedAt && <span> Posted {formatScheduledAt(post.postedAt)}</span>}
          {post.lemmyPostUrl && isHttpUrl(post.lemmyPostUrl) && (
            <>
              {" "}
              <a href={post.lemmyPostUrl} target="_blank" rel="noopener noreferrer">
                View on Lemmy
              </a>
            </>
          )}
        </p>
      )}
      {!editable && (
        <p className="state state-info" role="status">
          {readOnlyReason(status)}
        </p>
      )}
      <p className="muted">
        Concurrent edits aren&apos;t detected: if you edit this post elsewhere, the last save wins.
      </p>
      {formError && (
        <p className="state state-error" role="alert">
          {formError}
        </p>
      )}

      <fieldset disabled={busy || !editable} className="fields">
        <Field label="Title" error={fieldErrors.title}>
          <input value={fields.title} onChange={(e) => update("title", e.target.value)} maxLength={200} />
        </Field>

        <Field label="Community" error={fieldErrors.community}>
          <input
            value={fields.community}
            onChange={(e) => update("community", e.target.value)}
            placeholder="community@instance.example"
          />
        </Field>

        <label className="checkbox">
          <input type="checkbox" checked={fields.nsfw} onChange={(e) => update("nsfw", e.target.checked)} /> NSFW
        </label>

        <Field label={`Scheduled time (${zone})`} error={fieldErrors.scheduled_at}>
          <span className="inline">
            <input
              type="datetime-local"
              value={fields.scheduledLocal}
              onChange={(e) => update("scheduledLocal", e.target.value)}
            />
            <button type="button" className="button" onClick={() => update("scheduledLocal", "")}>
              Clear
            </button>
          </span>
        </Field>

        <div className="link-or-image">
          <div className="link-col">
            <Field label="URL" error={urlError ?? fieldErrors.url}>
              <input
                type="url"
                value={fields.url}
                disabled={hasTitleImage && !hasUrl}
                aria-invalid={urlError !== undefined}
                onChange={(e) => update("url", e.target.value)}
              />
            </Field>
            {hasTitleImage && !hasUrl && (
              <p className="muted">
                A post shares a URL or a title image, not both. Remove the title image to use a URL.
              </p>
            )}
          </div>
          <TitleImagePanel
            previewUrl={titlePreview}
            uploadsOnSave={pendingTitle !== null}
            selectable={storedImages.filter((image) => image.id !== titleId)}
            locked={hasUrl && !hasTitleImage}
            onChoose={chooseTitleFile}
            onRemove={removeTitle}
            onUseExisting={useExistingTitle}
          />
        </div>
        {hasUrl && hasTitleImage && (
          <p className="state state-info">
            This post has both a URL and a title image. Lemmy posts use one or the other; remove one.
          </p>
        )}

        <Field label="Body" error={fieldErrors.body}>
          <textarea
            ref={bodyRef}
            rows={10}
            value={fields.body}
            onChange={(e) => update("body", e.target.value)}
            maxLength={10000}
          />
        </Field>

        <BodyImageList
          postId={post?.id}
          listedIds={listedIds}
          detachedIds={detachedIds}
          storedIds={storedIds}
          onAdd={addBodyImage}
          onInsert={insertExisting}
          onRemove={removeBodyImage}
        />
      </fieldset>

      {imageProblem && (
        <p className="state state-error" role="alert">
          {imageProblem}{" "}
          {pendingTitle && post && (
            <button type="button" className="button" onClick={() => save()} disabled={busy || !canSave}>
              Retry upload
            </button>
          )}
        </p>
      )}

      <BodyPreview body={fields.body} postId={post?.id} storedIds={storedIds} />

      <PostActions
        status={status}
        editable={editable}
        busy={busy}
        canSave={canSave}
        futureTime={futureTime}
        canDelete={post !== undefined && status !== ScheduledPostStatus.POSTING}
        onSave={() => save()}
        onAction={save}
        onDelete={remove}
      />
      {hint && <p className="muted">{hint}</p>}
      {dirty && !busy && <p className="muted">You have unsaved changes.</p>}
    </form>
  );
}
