/**
 * code-runner logomark — three forward slashes (///), motion toward the
 * wordmark. Achromatic by design: the slashes are `currentColor`, so the mark
 * inherits ink in light, near-white in dark, and emphasis-ink on dark panels.
 * The original three-tone fade is preserved through opacity, not hue — iris
 * stays rationed, no second brand color enters via the logo.
 *
 * Decorative by default (`aria-hidden`): it always sits beside the text
 * wordmark, which carries the accessible name.
 */
export function Logomark({ className }: { className?: string }) {
  return (
    <svg viewBox="0 0 78 32" fill="none" aria-hidden className={className}>
      <path d="M55.5 0H77.5L58.5 32H36.5L55.5 0Z" fill="currentColor" />
      <path
        d="M35.5 0H51.5L32.5 32H16.5L35.5 0Z"
        fill="currentColor"
        opacity={0.62}
      />
      <path
        d="M19.5 0H31.5L12.5 32H0.5L19.5 0Z"
        fill="currentColor"
        opacity={0.4}
      />
    </svg>
  );
}
