import { useId } from "react";

type BrandMarkProps = {
  className?: string;
  size?: number;
  title?: string;
};

/**
 * The moyro mark stays inline so it is crisp at every UI density and does not
 * delay the first-paint brand surface. Leave `title` empty when adjacent text
 * already names the service; otherwise it becomes the accessible image name.
 */
export function BrandMark({ className, size = 40, title }: BrandMarkProps) {
  const id = useId().replace(/:/g, "");
  const gradientID = `moyro-mark-${id}`;
  const titleID = `moyro-mark-title-${id}`;

  return (
    <svg
      className={className}
      width={size}
      height={size}
      viewBox="0 0 64 64"
      role={title ? "img" : undefined}
      aria-labelledby={title ? titleID : undefined}
      aria-hidden={title ? undefined : true}
      focusable="false"
    >
      {title && <title id={titleID}>{title}</title>}
      <defs>
        <linearGradient id={gradientID} x1="8" y1="6" x2="56" y2="60" gradientUnits="userSpaceOnUse">
          <stop stopColor="#315FEA" />
          <stop offset="1" stopColor="#6849D8" />
        </linearGradient>
      </defs>
      <rect width="64" height="64" rx="16" fill={`url(#${gradientID})`} />
      <path
        d="M12 4.5C26-1 48 3 59.5 17"
        fill="none"
        stroke="#fff"
        strokeOpacity=".13"
        strokeWidth="7"
        strokeLinecap="round"
      />
      <path
        d="M16 44V26c0-5.6 3-9 7.2-9 3 0 5.1 1.7 6.8 4.2l2 3 2-3c1.7-2.5 3.8-4.2 6.8-4.2 4.2 0 7.2 3.4 7.2 9v18"
        fill="none"
        stroke="#fff"
        strokeWidth="7.5"
        strokeLinecap="round"
        strokeLinejoin="round"
      />
      <circle cx="32" cy="24.2" r="2.8" fill="#72F0C9" />
    </svg>
  );
}
