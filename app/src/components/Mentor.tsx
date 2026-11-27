import type { ReactNode } from 'react';

/** Carmen's note, as on the O3 boards. Her portrait is not in the repo yet, so an initial stands in. */
export function Mentor({ children }: { children: ReactNode }) {
  return (
    <div className="flex items-start gap-3">
      <span aria-hidden="true" className="inline-flex size-11 flex-none items-center justify-center rounded-full bg-accentsoft text-base font-extrabold text-accentink">
        C
      </span>
      <p className="m-0 flex-1 rounded-xl border border-line bg-surface px-3 py-2.5 text-sm leading-normal">
        <b>Carmen</b> · {children}
      </p>
    </div>
  );
}
