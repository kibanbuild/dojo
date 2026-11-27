import type { ReactNode } from 'react';
import { Logo } from './Logo';

/** Day one, as on the O0 to O5 boards. `step` is 1 to 6; O1 (sign in) is step 2 and lives on its own page. */
export const DAY_ONE_STEPS = ['Path', 'Sign in', 'Install', 'Doctor', 'SSH key', 'First ticket'] as const;

export function OnboardingFrame({ step, hint, children }: { step: number; hint?: string; children: ReactNode }) {
  const label = DAY_ONE_STEPS[step - 1] ?? '';
  return (
    <div className="min-h-screen bg-bg font-sans text-fg">
      <header className="border-b border-line bg-bg">
        <div className="mx-auto flex max-w-[1200px] items-center justify-between gap-4 px-4 py-3 md:px-6 md:py-3.5">
          <div className="flex items-center gap-2.5">
            <Logo size={28} stroke={2.66} />
            <span className="font-mono text-base font-semibold md:text-lg">dojo</span>
          </div>
          <ol aria-label="Day one progress" className="m-0 hidden list-none items-center gap-2.5 p-0 md:flex">
            {DAY_ONE_STEPS.map((name, i) => {
              const n = i + 1;
              const done = n < step;
              const current = n === step;
              return (
                <li key={name} aria-current={current ? 'step' : undefined} className="flex items-center gap-2.5">
                  {i > 0 && <span aria-hidden="true" className="h-px w-7 bg-line" />}
                  <span className={`flex items-center gap-2 text-[13px] ${current ? 'font-extrabold text-fg' : 'font-semibold text-muted'}`}>
                    <span
                      className={`inline-flex size-[22px] items-center justify-center rounded-full text-xs ${
                        current ? 'bg-accent font-extrabold text-accentfg' : done ? 'bg-ok font-extrabold text-white' : 'border border-line font-bold'
                      }`}
                    >
                      {done ? <span aria-hidden="true">✓</span> : n}
                    </span>
                    {name}
                    {done && <span className="sr-only"> (done)</span>}
                  </span>
                </li>
              );
            })}
          </ol>
        </div>
      </header>
      <main className="mx-auto flex max-w-[1200px] flex-col gap-[18px] px-4 pt-5 pb-7 md:gap-6 md:px-6 md:pt-9 md:pb-12">
        <div className="flex flex-col gap-2 md:hidden">
          <div className="flex justify-between text-xs font-bold">
            <span>Day one · step {step} of 6</span>
            <span className="text-muted">{label}</span>
          </div>
          <div aria-hidden="true" className="grid grid-cols-6 gap-1">
            {DAY_ONE_STEPS.map((name, i) => (
              <span key={name} className={`h-1 rounded-sm ${i < step ? 'bg-accent' : 'bg-line'}`} />
            ))}
          </div>
        </div>
        <div className="hidden font-mono text-[13px] text-muted md:block">
          step {step} of 6 · {hint ?? label.toLowerCase()}
        </div>
        {children}
      </main>
    </div>
  );
}
