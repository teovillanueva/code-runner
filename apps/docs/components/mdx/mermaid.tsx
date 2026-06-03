'use client';

import { useEffect, useId, useRef, useState } from 'react';

/**
 * Client-rendered Mermaid diagram, themed to follow the Fumadocs light/dark
 * toggle (the `dark` class on <html>). Falls back to the raw source on error.
 */
export function Mermaid({ chart }: { chart: string }) {
  const rawId = useId();
  const safeId = `mmd-${rawId.replace(/[^a-zA-Z0-9]/g, '')}`;
  const seq = useRef(0);
  const [svg, setSvg] = useState('');
  const [failed, setFailed] = useState(false);

  useEffect(() => {
    let active = true;

    async function render() {
      const { default: mermaid } = await import('mermaid');
      const isDark = document.documentElement.classList.contains('dark');

      mermaid.initialize({
        startOnLoad: false,
        securityLevel: 'loose',
        theme: isDark ? 'dark' : 'neutral',
        themeVariables: {
          fontFamily: 'inherit',
          primaryColor: isDark ? '#0d1117' : '#f6f8fa',
          primaryBorderColor: isDark ? '#34d399' : '#059669',
          primaryTextColor: isDark ? '#e6edf3' : '#0b1220',
          lineColor: isDark ? '#22d3ee' : '#0891b2',
          secondaryColor: isDark ? '#11161f' : '#eef2f6',
          tertiaryColor: isDark ? '#0b1018' : '#f1f5f9',
        },
      });

      try {
        seq.current += 1;
        const { svg } = await mermaid.render(`${safeId}-${seq.current}`, chart.trim());
        if (active) {
          setSvg(svg);
          setFailed(false);
        }
      } catch {
        if (active) setFailed(true);
      }
    }

    void render();

    // re-render when the user toggles light/dark
    const observer = new MutationObserver(() => void render());
    observer.observe(document.documentElement, {
      attributes: true,
      attributeFilter: ['class'],
    });

    return () => {
      active = false;
      observer.disconnect();
    };
  }, [chart, safeId]);

  if (failed) {
    return (
      <pre className="overflow-x-auto rounded-lg border bg-fd-secondary p-4 text-sm">
        <code>{chart.trim()}</code>
      </pre>
    );
  }

  return (
    <div
      className="my-6 flex justify-center overflow-x-auto rounded-xl border bg-fd-card/40 p-4 [&_svg]:h-auto [&_svg]:max-w-full"
      // eslint-disable-next-line react/no-danger
      dangerouslySetInnerHTML={{ __html: svg }}
    />
  );
}
