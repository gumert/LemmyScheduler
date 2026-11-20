import { bodyImageUrl } from "../api/client";
import { splitBody } from "../body-images";

type Props = {
  body: string;
  // Undefined until the post has been saved once; its images can't render before that.
  postId?: bigint;
  storedIds: Set<bigint>;
};

export function BodyPreview({ body, postId, storedIds }: Props) {
  if (!body) {
    return null;
  }
  return (
    <section className="preview">
      <h2>Body preview</h2>
      <div className="preview-body">
        {postId === undefined
          ? body
          : splitBody(body).map((segment, index) =>
              segment.type === "text" ? (
                <span key={index}>{segment.text}</span>
              ) : storedIds.has(segment.id) ? (
                // eslint-disable-next-line @next/next/no-img-element -- API-served bytes
                <img key={index} src={bodyImageUrl(postId, segment.id)} alt="" loading="lazy" />
              ) : (
                <em key={index}>[missing image {String(segment.id)}]</em>
              ),
            )}
      </div>
    </section>
  );
}
