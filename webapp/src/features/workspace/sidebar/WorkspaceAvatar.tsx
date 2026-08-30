import { useEffect, useState } from "react";
import type { UserStatusValue } from "@/api/client";
import { api } from "@/api/client";
import { AuthenticatedImage, isExternalImageURL } from "@/components/AuthenticatedMedia";

function avatarColor(id: string): string {
  const palette = ["#6366f1", "#8b5cf6", "#ec4899", "#f59e0b", "#10b981", "#06b6d4"];
  let hash = 0;
  for (let index = 0; index < id.length; index += 1) {
    hash = (hash * 31 + id.charCodeAt(index)) >>> 0;
  }
  return palette[hash % palette.length];
}

export type WorkspaceAvatarProps = {
  token: string | null;
  id: string;
  name: string;
  status?: UserStatusValue;
  size?: number;
  picture?: string;
  updateAt?: number;
};

/**
 * Authenticated workspace avatar shared by the sidebar, channel header and
 * message surfaces. The legacy `.avatar` contract is intentionally retained.
 */
export function WorkspaceAvatar({
  token,
  id,
  name,
  status,
  size = 28,
  picture,
  updateAt,
}: WorkspaceAvatarProps) {
  const background = avatarColor(id || name || "?");
  const initial = (name || id || "?")[0]?.toUpperCase() ?? "?";
  const [imageFailed, setImageFailed] = useState(false);
  const externalPicture = isExternalImageURL(picture);
  const showImage = Boolean(picture) && !imageFailed && Boolean(id) && (externalPicture || Boolean(token));

  useEffect(() => setImageFailed(false), [picture, updateAt, token]);

  return (
    <span
      className="avatar"
      style={{
        width: size,
        height: size,
        background: showImage ? "transparent" : background,
        fontSize: size * 0.45,
      }}
    >
      {showImage ? (
        externalPicture ? (
          <img
            src={picture}
            alt=""
            referrerPolicy="no-referrer"
            onError={() => setImageFailed(true)}
          />
        ) : (
          <AuthenticatedImage
            token={token ?? ""}
            path={api.userImagePath(id, updateAt ?? picture)}
            alt=""
            onFetchError={() => setImageFailed(true)}
            onError={() => setImageFailed(true)}
          />
        )
      ) : (
        initial
      )}
      {status && <span className={`status-dot status-${status}`} />}
    </span>
  );
}
