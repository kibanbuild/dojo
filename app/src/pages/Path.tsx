import { Link, Navigate } from 'react-router';
import { AppFrame } from '../components/AppFrame';
import type { Me } from '../lib/me';
import { READ_MESSAGES, ReadError, useCatalog, usePath, type LearnerPath, type PathItem, type PathRole, type TicketState } from '../lib/read';
import { useLiveData } from '../lib/stream';

type RoleStatus = 'passed' | 'here' | 'next' | 'soon';

const PILL = 'inline-flex h-[26px] flex-none items-center rounded-md px-2.5 text-xs font-bold whitespace-nowrap';
const META = 'font-mono text-xs text-muted';

// here/next are derived client-side because /v1/path has no current-role field; tickets are not gated.
/** First unpassed, unlocked role is "here"; later ones follow it; a locked role has no tickets yet. */
export function roleStatuses(roles: PathRole[]): RoleStatus[] {
  const here = roles.findIndex((r) => !r.passed && !r.locked);
  return roles.map((r, i) => {
    if (r.passed) return 'passed';
    if (r.locked) return 'soon';
    return i === here ? 'here' : 'next';
  });
}

const ITEM_LABEL: Record<TicketState | 'none', string> = {
  passed: 'Passed',
  started: 'In progress',
  revisit: 'Revisit',
  none: 'Not started',
};

function Marker({ status }: { status: RoleStatus }) {
  const base = 'inline-flex size-8 flex-none items-center justify-center rounded-full text-sm font-extrabold';
  if (status === 'passed') {
    return (
      <span aria-hidden="true" className={`${base} bg-ok text-white`}>
        ✓
      </span>
    );
  }
  if (status === 'here') {
    return (
      <span aria-hidden="true" className={`${base} bg-accent text-white`}>
        ●
      </span>
    );
  }
  return (
    <span className={`${base} box-border border-2 border-ctl bg-surface text-muted`}>
      <span className="sr-only">{status === 'soon' ? 'coming soon' : 'next'}</span>
    </span>
  );
}

function Items({ items }: { items: PathItem[] }) {
  return (
    <ol className="m-0 mt-1 flex list-none flex-col p-0">
      {items.map((it) => {
        const label = ITEM_LABEL[it.state ?? 'none'];
        return (
          <li key={it.id}>
            <Link
              to={`/t/${it.id}`}
              className="flex min-h-11 items-center justify-between gap-3 rounded-[10px] px-2 text-sm text-fg no-underline hover:bg-surface2"
            >
              <span>
                <span className="font-mono text-xs text-muted">{it.order}. </span>
                {it.title}
              </span>
              <span className={`font-mono text-xs ${it.state === 'passed' ? 'text-ok' : 'text-muted'}`}>{label}</span>
            </Link>
          </li>
        );
      })}
    </ol>
  );
}

function RoleRow({ role, status, hereTitle, hasExam, last }: { role: PathRole; status: RoleStatus; hereTitle: string | null; hasExam: boolean; last: boolean }) {
  const pill =
    status === 'passed' ? (
      <span className={`${PILL} bg-oksoft text-ok`}>Passed</span>
    ) : status === 'here' ? (
      <span className={`${PILL} bg-accentsoft text-accentink`}>You are here</span>
    ) : null;
  const note =
    status === 'soon'
      ? 'Coming soon'
      : status === 'next'
        ? hereTitle
          ? `after ${hereTitle}`
          : null
        : null;
  const hasTickets = role.tickets_total > 0 && status !== 'soon';
  return (
    <li className="relative flex gap-3.5">
      {!last && <span aria-hidden="true" className="absolute top-8 -bottom-0 left-[15px] w-0.5 bg-line" />}
      <Marker status={status} />
      <div className="flex min-w-0 flex-1 flex-col gap-1 pb-6">
        <div className="flex flex-wrap items-center gap-2.5">
          <h2 className="m-0 text-lg font-extrabold">{role.title}</h2>
          {pill}
        </div>
        {hasExam && <div className={META}>ends in a role exam: one work day</div>}
        {role.tickets_total > 0 && (
          <div className="font-mono text-xs text-accentink">
            {role.tickets_passed} of {role.tickets_total} tickets
          </div>
        )}
        {note && <div className="font-mono text-xs text-accentink">{note}</div>}
        {hasTickets && (
          <details className="group mt-1">
            <summary className="flex min-h-11 w-fit cursor-pointer items-center text-[13px] font-bold text-accentink">
              Tickets in {role.title}
            </summary>
            <Items items={role.items} />
          </details>
        )}
      </div>
    </li>
  );
}

function Timeline({ path }: { path: LearnerPath }) {
  const catalog = useCatalog();
  const exam = new Set(catalog.data?.roles.filter((r) => r.has_exam).map((r) => r.slug));
  const pathTitle = catalog.data?.paths.find((p) => p.slug === path.path)?.title ?? null;
  const statuses = roleStatuses(path.roles);
  const hereIdx = statuses.indexOf('here');
  const hereTitle = path.roles[hereIdx]?.title ?? null;
  const target = path.roles.at(-1)?.title ?? '';
  return (
    <>
      <div className="min-h-5 font-mono text-[13px] text-muted">{pathTitle ? `path · ${pathTitle}` : ''}</div>
      <h1 className="m-0 mt-3 text-[30px] leading-[1.1] font-extrabold tracking-tight md:text-[44px]">
        Your way to <span className="bg-(image:--hlbg) bg-clip-text text-transparent">{target}</span>
      </h1>
      <p className="mt-4 mb-8 max-w-[60ch] text-[15px] leading-[1.55] text-fg2">
        {path.roles.length === 1 ? 'One role' : `${numberWord(path.roles.length)} roles`}, each a real job. Pass a role's work-day exam
        and you are ready for that job, even if you stop there.
      </p>
      <ol aria-label="Roles on your path" className="m-0 flex max-w-[640px] list-none flex-col p-0">
        {path.roles.map((role, i) => (
          <RoleRow key={role.slug} role={role} status={statuses[i] ?? 'next'} hereTitle={hereTitle} hasExam={exam.has(role.slug)} last={i === path.roles.length - 1} />
        ))}
      </ol>
    </>
  );
}

const WORDS = ['Zero', 'One', 'Two', 'Three', 'Four', 'Five', 'Six', 'Seven', 'Eight', 'Nine'];
const numberWord = (n: number) => WORDS[n] ?? String(n);

function Body() {
  useLiveData();
  const path = usePath();
  if (path.isPending) return <p role="status" className="text-muted">Loading your path...</p>;
  if (path.isError) {
    const err = path.error;
    if (err instanceof ReadError && err.reason === 'signed_out') return <Navigate to="/signin" replace />;
    const reason = err instanceof ReadError ? err.reason : 'failed';
    return (
      <div role="alert">
        <p className="text-bad">{READ_MESSAGES[reason]}</p>
        <button type="button" onClick={() => void path.refetch()} className="h-11 rounded-[10px] border border-line bg-surface px-3.5 text-sm font-bold md:h-9">
          Retry
        </button>
      </div>
    );
  }
  if (path.data.path === null || path.data.roles.length === 0) {
    return (
      <div>
        <h1 className="m-0 text-[30px] font-extrabold tracking-tight">Your path</h1>
        <p className="mt-3 text-fg2">No path is open yet. Check back soon.</p>
      </div>
    );
  }
  return <Timeline path={path.data} />;
}

/** R1: the learner's path as a vertical role timeline. */
export function PathPage({ me }: { me: Me }) {
  return (
    <AppFrame me={me}>
      <Body />
    </AppFrame>
  );
}
