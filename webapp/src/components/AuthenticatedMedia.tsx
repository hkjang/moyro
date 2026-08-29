import { useEffect, useRef, useState, type ImgHTMLAttributes } from "react";
import { api } from "@/api/client";

type AuthenticatedImageProps = Omit<ImgHTMLAttributes<HTMLImageElement>, "src"> & {
  token: string;
  path: string;
  onFetchError?: () => void;
};

export function isExternalImageURL(value: string | undefined): value is string {
  if (!value) return false;
  try {
    const parsed = new URL(value);
    return parsed.protocol === "https:" || parsed.protocol === "http:";
  } catch {
    return false;
  }
}

/**
 * Renders protected media without placing a bearer token in the DOM or URL.
 * The server response is read with an Authorization header, then exposed to
 * the image element only as a short-lived, same-document blob URL.
 */
export function AuthenticatedImage({ token, path, alt = "", onFetchError, ...imageProps }: AuthenticatedImageProps) {
  const [objectURL, setObjectURL] = useState<string>();
  const [loading, setLoading] = useState(true);
  const onFetchErrorRef = useRef(onFetchError);
  onFetchErrorRef.current = onFetchError;

  useEffect(() => {
    let active = true;
    let allocatedURL: string | undefined;
    setObjectURL(undefined);
    setLoading(true);

    api.authenticatedMediaBlob(token, path).then(
      (blob) => {
        allocatedURL = URL.createObjectURL(blob);
        if (active) {
          setObjectURL(allocatedURL);
          setLoading(false);
        } else {
          URL.revokeObjectURL(allocatedURL);
        }
      },
      () => {
        if (active) {
          setObjectURL(undefined);
          setLoading(false);
          onFetchErrorRef.current?.();
        }
      },
    );

    return () => {
      active = false;
      if (allocatedURL) URL.revokeObjectURL(allocatedURL);
    };
  }, [token, path]);

  // An omitted src does not trigger a request for the current document. It
  // also preserves the element's layout/alt text while the blob is loading.
  return <img {...imageProps} src={objectURL} alt={alt} aria-busy={loading} />;
}

/** Fetches protected bytes, starts a browser download from a blob URL, and
 * revokes that URL immediately after the browser has consumed the click. */
export async function downloadAuthenticatedMedia(token: string, path: string, filename: string) {
  const blob = await api.authenticatedMediaBlob(token, path);
  const objectURL = URL.createObjectURL(blob);
  const anchor = document.createElement("a");
  anchor.href = objectURL;
  anchor.download = filename;
  anchor.rel = "noopener noreferrer";
  document.body.appendChild(anchor);
  try {
    anchor.click();
  } finally {
    anchor.remove();
    window.setTimeout(() => URL.revokeObjectURL(objectURL), 1000);
  }
}
