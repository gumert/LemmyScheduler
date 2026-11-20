import { PostEditor } from "../../../src/components/post-editor";

export default async function PostPage({ params }: { params: Promise<{ id: string }> }) {
  const { id } = await params;
  return (
    <main>
      <PostEditor id={id} />
    </main>
  );
}
