import Image from 'next/image';
import Link from 'next/link';
import { ArrowRight } from 'lucide-react';

/**
 * Production proof — the two surfaces that show code-runner already runs real,
 * high-stakes workloads:
 *
 *   <LogoCloud/>   a quiet hairline band beneath the language strip.
 *   <CaseStudy/>   the edalef × Universidad de San Andrés exam platform.
 *
 * The partner logos are flat achromatic PNGs (black on transparent), so they
 * sit inside the same achromatic system as the rest of the site: rendered as
 * ink in light, inverted to near-white in dark via `dark:invert`. No second
 * brand color enters through a logo; the iris stays rationed.
 */

const EdalefMark = ({ className }: { className?: string }) => (
  <Image
    src="/partners/edalef.png"
    alt=""
    width={486}
    height={518}
    aria-hidden
    className={className}
  />
);

const UdesaLogo = ({ className }: { className?: string }) => (
  <Image
    src="/partners/udesa.png"
    alt="Universidad de San Andrés"
    width={550}
    height={100}
    className={className}
  />
);

export function LogoCloud() {
  return (
    <section className="flex flex-col items-start gap-6 border-t border-line px-5 py-7 sm:flex-row sm:items-center sm:justify-between sm:gap-10 sm:px-8">
      <p className="max-w-[24ch] shrink-0 font-mono text-[11px] uppercase leading-relaxed tracking-[0.14em] text-ink-3">
        Already running live exams in production
      </p>

      <ul className="flex flex-wrap items-center gap-x-10 gap-y-6">
        <li className="flex items-center gap-2.5 opacity-80 transition-opacity duration-150 ease-snap hover:opacity-100">
          <EdalefMark className="h-10 w-auto dark:invert" />
          <span className="text-[19px] font-semibold tracking-[-0.02em] text-ink">
            edalef
          </span>
        </li>
        <li className="opacity-80 transition-opacity duration-150 ease-snap hover:opacity-100">
          <UdesaLogo className="h-8 w-auto dark:invert" />
        </li>
      </ul>
    </section>
  );
}

const STATS = [
  { value: 'Thousands', label: 'exams administered' },
  { value: '1 : 1', label: 'hardened sandbox per student' },
  { value: '0', label: 'containers or slots leaked' },
];

export function CaseStudy() {
  return (
    <section className="border-t border-line px-5 py-16 sm:px-8 lg:py-20">
      <p className="font-mono text-[11px] uppercase tracking-[0.14em] text-iris">
        Case study
      </p>

      <div className="mt-4 grid grid-cols-1 items-start gap-10 lg:grid-cols-[1.05fr_0.95fr] lg:gap-14">
        <div>
          <h2 className="max-w-[20ch] text-balance text-[1.75rem] font-semibold leading-[1.13] tracking-[-0.02em]">
            Thousands of live coding exams, graded as the code runs.
          </h2>
          <p className="mt-4 max-w-[52ch] text-pretty leading-relaxed text-ink-2">
            <span className="font-medium text-ink">edalef</span> built its online
            examination platform on code-runner. At{' '}
            <span className="font-medium text-ink">
              Universidad de San Andrés
            </span>
            , students sit thousands of programming exams in it. Each one writes
            and runs code inside its own hardened sandbox, with stdin sent and
            output streamed live while the process is still running. Every session
            is bounded by the three clocks and reaped the moment it ends, so no
            exam ever leaks a container or holds a slot it no longer needs.
          </p>

          <dl className="mt-8 grid grid-cols-[max-content_1fr] items-baseline overflow-hidden rounded-card border border-line bg-card">
            {STATS.map((s, i) => (
              <div key={s.label} className="contents">
                <dt
                  className={`px-5 py-3.5 font-mono text-[1.0625rem] leading-none tracking-[-0.01em] text-ink ${
                    i > 0 ? 'border-t border-line' : ''
                  }`}
                >
                  {s.value}
                </dt>
                <dd
                  className={`px-5 py-3.5 text-[13px] leading-snug text-ink-2 ${
                    i > 0 ? 'border-t border-line' : ''
                  }`}
                >
                  {s.label}
                </dd>
              </div>
            ))}
          </dl>

          <Link
            href="/docs/guides/interactive-stdin"
            className="group mt-7 inline-flex items-center gap-1.5 font-mono text-[13px] text-iris transition-colors duration-150 ease-snap hover:text-iris-hover focus-visible:rounded-chip focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-iris"
          >
            How the live session works
            <ArrowRight className="size-3.5 transition-transform duration-150 ease-snap group-hover:translate-x-0.5" />
          </Link>
        </div>

        {/* partner card — who runs it, and on what */}
        <div className="rounded-card border border-line bg-section p-7 sm:p-8">
          <p className="font-mono text-[12px] text-ink-3">running on code-runner</p>

          <div className="mt-7 flex items-center gap-3.5">
            <EdalefMark className="h-11 w-auto dark:invert" />
            <div className="flex flex-col">
              <span className="text-[22px] font-semibold leading-none tracking-[-0.02em] text-ink">
                edalef
              </span>
              <span className="mt-1.5 text-[13px] text-ink-3">
                online examination platform
              </span>
            </div>
          </div>

          <div className="my-7 h-px bg-line" />

          <UdesaLogo className="h-9 w-auto dark:invert" />
          <p className="mt-3 text-[13px] text-ink-3">
            Universidad de San Andrés · Buenos Aires, Argentina
          </p>
        </div>
      </div>
    </section>
  );
}
