import { useEffect, useRef, useState } from "react";

export type PendingImage = { file: File; previewUrl: string };

// Holds a chosen image file with an object URL for previewing it, and revokes
// the URL when the file is replaced, cleared or the component unmounts.
export function usePendingImage() {
  const [pending, setPending] = useState<PendingImage | null>(null);
  const urlRef = useRef<string | null>(null);

  function revoke() {
    if (urlRef.current) {
      URL.revokeObjectURL(urlRef.current);
      urlRef.current = null;
    }
  }

  useEffect(() => revoke, []);

  function choose(file: File) {
    revoke();
    urlRef.current = URL.createObjectURL(file);
    setPending({ file, previewUrl: urlRef.current });
  }

  function clear() {
    revoke();
    setPending(null);
  }

  return { pending, choose, clear };
}
