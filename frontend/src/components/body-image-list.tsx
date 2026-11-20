import { bodyImageUrl } from "../api/client";
import { imageToken } from "../body-images";
import { IMAGE_ACCEPT } from "../images";

type Props = {
  // Undefined until the post has been saved once; images belong to a stored post.
  postId?: bigint;
  // Images referenced by the body followed by stored images it no longer references.
  listedIds: bigint[];
  detachedIds: bigint[];
  storedIds: Set<bigint>;
  onAdd: (file: File) => void;
  onInsert: (id: bigint) => void;
  onRemove: (id: bigint) => void;
};

export function BodyImageList({ postId, listedIds, detachedIds, storedIds, onAdd, onInsert, onRemove }: Props) {
  return (
    <div className="images">
      <h2>Body images</h2>
      {postId !== undefined ? (
        <label className="button">
          Add image to body…
          <input
            type="file"
            accept={IMAGE_ACCEPT}
            hidden
            onChange={(e) => {
              const file = e.target.files?.[0];
              if (file) {
                onAdd(file);
              }
              e.target.value = "";
            }}
          />
        </label>
      ) : (
        <p className="muted">Save the post first, then you can add images to the body.</p>
      )}
      <p className="muted">Adding or removing a body image saves the post immediately.</p>
      {listedIds.length > 0 && (
        <ul className="body-images">
          {listedIds.map((id) => {
            const detached = detachedIds.includes(id);
            return (
              <li key={String(id)}>
                {postId !== undefined && storedIds.has(id) ? (
                  // eslint-disable-next-line @next/next/no-img-element -- API-served bytes
                  <img src={bodyImageUrl(postId, id)} alt={`Body image ${id}`} loading="lazy" />
                ) : (
                  <span className="muted">Image {String(id)} isn&apos;t stored for this post.</span>
                )}
                <span className="image-meta">
                  <code>({imageToken(id)})</code>
                  {detached && <span className="muted">Not in the body. The next save deletes it.</span>}
                </span>
                <span className="inline">
                  {detached && (
                    <button type="button" className="button" onClick={() => onInsert(id)}>
                      Insert
                    </button>
                  )}
                  <button type="button" className="button" onClick={() => onRemove(id)}>
                    Remove
                  </button>
                </span>
              </li>
            );
          })}
        </ul>
      )}
    </div>
  );
}
