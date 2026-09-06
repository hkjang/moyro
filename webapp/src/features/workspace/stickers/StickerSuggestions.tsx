import CloseRounded from "@mui/icons-material/CloseRounded";
import { BuiltinSticker } from "./Sticker";
import { BUILTIN_PREFIX, type StickerSpec } from "./stickers";

/**
 * The strip of emoticons offered while the reader types. One tap sends the
 * emoticon; the strip never steals focus from the textarea, so typing on
 * simply refines or dismisses it.
 */
export function StickerSuggestions({ suggestions, onPick, onDismiss }: {
  suggestions: StickerSpec[];
  onPick: (spec: StickerSpec) => void;
  onDismiss: () => void;
}) {
  if (suggestions.length === 0) return null;
  return (
    <div className="sticker-suggestions" role="group" aria-label="추천 이모티콘">
      <span className="sticker-suggestions-label">이모티콘</span>
      <div className="sticker-suggestions-list">
        {suggestions.map((spec) => (
          <button
            key={spec.id}
            type="button"
            className="sticker-suggestion"
            title={`${spec.caption} 이모티콘 보내기`}
            aria-label={`${spec.caption} 이모티콘 보내기`}
            data-sticker-id={`${BUILTIN_PREFIX}${spec.id}`}
            onMouseDown={(event) => event.preventDefault()}
            onClick={() => onPick(spec)}
          >
            <BuiltinSticker spec={spec} size={52} showCaption={false} />
            <span className="sticker-suggestion-caption">{spec.caption}</span>
          </button>
        ))}
      </div>
      <button
        type="button"
        className="action-btn sticker-suggestions-close"
        aria-label="추천 이모티콘 닫기"
        title="닫기"
        onMouseDown={(event) => event.preventDefault()}
        onClick={onDismiss}
      >
        <CloseRounded fontSize="inherit" aria-hidden />
      </button>
    </div>
  );
}
