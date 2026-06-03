'use client';

/**
 * The hero artifact: one interactive sandbox session that plays itself.
 *
 * SSR renders the *finished* session — fully readable with JS off or motion
 * disabled, no layout shift. On mount, if motion is allowed, it resets and
 * auto-plays the round-trip on a loop: request → sandbox boot → the program
 * blocks on input() → stdin is written into the live field mid-run (the one
 * thing a batch runner can't do) → output streams → exit 0.
 *
 * Every stream row is always in the DOM; reveal only toggles opacity, so the
 * panel height is constant (zero CLS) and lines stream into a stable box.
 */

import { useEffect, useLayoutEffect, useRef, useState } from 'react';

const useIsoLayoutEffect =
  typeof window !== 'undefined' ? useLayoutEffect : useEffect;

type Tone = 'meta' | 'live' | 'prompt' | 'in' | 'ok';

const LINES: { gutter: string; live?: boolean; text: string; tone: Tone }[] = [
  { gutter: '→', text: 'POST /run   { language: "python", version: "3.12" }', tone: 'meta' },
  { gutter: '→', text: 'job_8f3a · queued · slot 4/16 acquired', tone: 'meta' },
  { gutter: '●', live: true, text: 'sandbox up · network=none · ro-rootfs · seccomp', tone: 'live' },
  { gutter: '>>>', text: 'who goes there? ', tone: 'prompt' },
  { gutter: '‹', live: true, text: 'ada', tone: 'in' },
  { gutter: ' ', text: 'hello, ada', tone: 'ok' },
];

const STAMPS = ['0.00s', '0.01s', '0.04s', '0.05s', '0.05s', '0.06s'];
const FULL = LINES.length;

const TONE: Record<Tone, string> = {
  meta: 'text-emphasis-ink/55',
  live: 'text-status-running',
  prompt: 'text-emphasis-ink/85',
  in: 'text-emphasis-ink',
  ok: 'text-status-ok',
};

const STDIN_WORD = 'ada';

export function HeroTerminal() {
  // Initial = finished session (matches SSR; survives JS-off / reduced-motion).
  const [revealed, setRevealed] = useState(FULL);
  const [stdin, setStdin] = useState('');
  const [typing, setTyping] = useState(false);
  const [done, setDone] = useState(true);
  const stoppedRef = useRef(false);

  useIsoLayoutEffect(() => {
    if (window.matchMedia('(prefers-reduced-motion: reduce)').matches) return;

    let timers: number[] = [];
    stoppedRef.current = false;
    const clearAll = () => {
      timers.forEach(clearTimeout);
      timers = [];
    };

    const cycle = () => {
      if (stoppedRef.current) return;
      clearAll();
      let t = 0;
      const at = (dt: number, fn: () => void) => {
        t += dt;
        timers.push(
          window.setTimeout(() => {
            if (!stoppedRef.current) fn();
          }, t),
        );
      };

      setRevealed(0);
      setStdin('');
      setTyping(false);
      setDone(false);

      at(280, () => setRevealed(1)); // POST /run
      at(190, () => setRevealed(2)); // queued
      at(340, () => setRevealed(3)); // sandbox up
      at(520, () => setRevealed(4)); // program prints prompt, then blocks
      at(300, () => setTyping(true)); // field goes live — process is waiting
      at(360, () => setStdin('a')); // stdin written, mid-run
      at(118, () => setStdin('ad'));
      at(122, () => setStdin('ada'));
      at(440, () => {
        // "enter" — stdin consumed, echo lands in the stream
        setStdin('');
        setTyping(false);
        setRevealed(5);
      });
      at(300, () => setRevealed(6)); // hello, ada
      at(440, () => setDone(true)); // exit 0
      at(3000, cycle); // hold, then replay
    };

    // Clean slate before first paint so the finished session never flashes.
    setRevealed(0);
    setStdin('');
    setTyping(false);
    setDone(false);
    timers.push(window.setTimeout(cycle, 360));

    return () => {
      stoppedRef.current = true;
      clearAll();
    };
  }, []);

  const elapsed = done ? STAMPS[FULL - 1] : STAMPS[Math.max(0, revealed - 1)];
  const fieldLive = typing || stdin.length > 0;

  return (
    <section
      aria-label="Example code-runner session: a live, interactive stdin round-trip"
      className="cr-on-dark overflow-hidden rounded-card border border-line bg-emphasis text-emphasis-ink"
    >
      {/* window chrome / status line */}
      <div className="flex items-center justify-between border-b border-white/[0.08] px-4 py-3">
        <div className="flex items-center gap-2.5">
          <span className="font-mono text-[12px] text-emphasis-ink/55">
            interactive session
          </span>
          <span className="font-mono text-[11px] text-emphasis-ink/30">
            job_8f3a
          </span>
        </div>
        <div className="flex items-center gap-3 font-mono text-[11px]">
          <span className="tabular-nums text-emphasis-ink/40">{elapsed}</span>
          <span aria-hidden className="text-emphasis-ink/15">
            |
          </span>
          {done ? (
            <span className="inline-flex items-center gap-1.5 text-emphasis-ink/45">
              <span
                aria-hidden
                className="size-[7px] rounded-full bg-emphasis-ink/30"
              />
              done
            </span>
          ) : (
            <span className="inline-flex items-center gap-1.5 text-status-running">
              <span className="cr-dot" aria-hidden />
              running
            </span>
          )}
        </div>
      </div>

      {/* stream — all rows present; reveal toggles opacity, so height is fixed */}
      <div className="px-4 py-4 font-mono text-[12.5px] leading-[1.75] sm:text-[13px]">
        {LINES.map((l, i) => (
          <div
            key={l.text}
            data-shown={i < revealed}
            className="flex gap-2.5 transition duration-300 ease-snap data-[shown=false]:translate-y-[3px] data-[shown=false]:opacity-0 data-[shown=true]:translate-y-0 data-[shown=true]:opacity-100"
          >
            <span
              aria-hidden
              className={`w-[3ch] shrink-0 select-none text-right ${
                l.live ? 'text-status-running' : 'text-emphasis-ink/25'
              }`}
            >
              {l.gutter}
            </span>
            <span className={`${TONE[l.tone]} text-pretty`}>{l.text}</span>
          </div>
        ))}
      </div>

      {/* the live stdin field — where input is written while the process runs */}
      <div
        data-live={fieldLive}
        className="flex items-center gap-2.5 border-t border-white/[0.08] bg-black/20 px-4 py-3 font-mono text-[12.5px] transition-colors duration-200 ease-snap data-[live=true]:bg-black/30 sm:text-[13px]"
      >
        <span className="select-none text-emphasis-ink/25" aria-hidden>
          {'>>>'}
        </span>
        <span className="flex-1 truncate">
          {fieldLive ? (
            <span className="text-emphasis-ink">
              {stdin}
              <span className="cr-caret align-middle" aria-hidden />
            </span>
          ) : (
            <span className="text-emphasis-ink/45">
              write to stdin, mid-run
              <span className="cr-caret align-middle" aria-hidden />
            </span>
          )}
        </span>
        <span
          className={`shrink-0 rounded-chip px-2 py-0.5 text-[11px] transition-colors duration-200 ease-snap ${
            fieldLive
              ? 'bg-iris-wash text-iris'
              : 'bg-white/[0.06] text-emphasis-ink/45'
          }`}
        >
          {fieldLive ? 'writing' : 'stdin open'}
        </span>
      </div>

      {/* result line — reserved height; fades in on exit */}
      <div
        data-shown={done}
        className="flex items-center gap-2.5 border-t border-white/[0.08] px-4 py-2.5 font-mono text-[11.5px] transition-opacity duration-300 ease-snap data-[shown=false]:opacity-0 data-[shown=true]:opacity-100"
      >
        <span className="rounded-chip bg-status-ok/15 px-2 py-0.5 text-status-ok">
          exit 0
        </span>
        <span className="text-emphasis-ink/50">
          41ms cpu · sandbox reaped · slot freed
        </span>
      </div>
    </section>
  );
}
