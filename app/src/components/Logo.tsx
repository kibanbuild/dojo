// The placeholder mark from the identity sheet; stroke width is the design's per size.
export function Logo({ size, stroke }: { size: number; stroke: number }) {
  const ink = { fill: 'var(--color-fg)', stroke: 'var(--color-fg)', strokeWidth: stroke, strokeLinejoin: 'miter' } as const;
  const accent = { fill: 'var(--color-accent)', stroke: 'var(--color-accent)', strokeWidth: stroke, strokeLinejoin: 'miter' } as const;
  return (
    <svg width={size} height={size} viewBox="0 0 64 64" aria-hidden="true" focusable="false">
      <polygon points="29.40,30.01 16.82,43.68 22.87,47.91 32.30,33.88" style={ink} />
      <polygon points="35.69,30.01 48.27,43.80 41.86,47.91 32.79,33.88" style={ink} />
      <polygon points="4.00,21.54 60.00,21.54 60.00,30.49 4.00,30.49" style={ink} />
      <polygon points="23.23,19.97 38.11,16.10 41.98,32.07 26.86,35.69" style={accent} />
    </svg>
  );
}
