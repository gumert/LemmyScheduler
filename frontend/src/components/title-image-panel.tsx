import { useState } from "react";

import { IMAGE_ACCEPT } from "../images";

type Props = {
  previewUrl?: string;
  uploadsOnSave: boolean;
  // Stored images that could become the title image.
  selectable: { id: bigint }[];
  // The post has a URL, and a post shares a URL or a title image, not both.
  locked: boolean;
  onChoose: (file: File) => void;
  onRemove: () => void;
  onUseExisting: (id: bigint) => void;
};

export function TitleImagePanel({
  previewUrl,
  uploadsOnSave,
  selectable,
  locked,
  onChoose,
  onRemove,
  onUseExisting,
}: Props) {
  const [existingChoice, setExistingChoice] = useState("");

  return (
    <fieldset className="image-col" disabled={locked}>
      <div className="images">
        <h2>Title image</h2>
        {previewUrl ? (
          // eslint-disable-next-line @next/next/no-img-element -- local preview or API-served bytes
          <img className="title-preview" src={previewUrl} alt="Title image preview" />
        ) : (
          <p className="muted">No title image.</p>
        )}
        {uploadsOnSave && <p className="muted">Will upload when you save the post.</p>}
        <div className="inline">
          <label className="button">
            {previewUrl ? "Replace…" : "Choose file…"}
            <input
              type="file"
              accept={IMAGE_ACCEPT}
              hidden
              onChange={(e) => {
                const file = e.target.files?.[0];
                if (file) {
                  onChoose(file);
                }
                e.target.value = "";
              }}
            />
          </label>
          {previewUrl && (
            <button type="button" className="button" onClick={onRemove}>
              Remove
            </button>
          )}
        </div>
        {selectable.length > 0 && (
          <div className="inline">
            <select value={existingChoice} onChange={(e) => setExistingChoice(e.target.value)}>
              <option value="">Use an existing image…</option>
              {selectable.map((image) => (
                <option key={String(image.id)} value={String(image.id)}>
                  Image {String(image.id)}
                </option>
              ))}
            </select>
            <button
              type="button"
              className="button"
              disabled={!existingChoice}
              onClick={() => {
                onUseExisting(BigInt(existingChoice));
                setExistingChoice("");
              }}
            >
              Use as title image
            </button>
          </div>
        )}
      </div>
      {locked && (
        <p className="muted">A post shares a URL or a title image, not both. Clear the URL to add a title image.</p>
      )}
    </fieldset>
  );
}
