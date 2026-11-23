import { useEffect, useId, useRef, useState, type RefObject } from 'react';
import { useQuery, useQueryClient } from '@tanstack/react-query';
import { SettingsLayout } from '../components/SettingsLayout';
import { ME_KEY, type Me } from '../lib/me';
import {
  DEVICES_KEY,
  DEVICES_MESSAGES,
  DevicesError,
  REVOKE_MESSAGES,
  fetchDevices,
  osLabel,
  relativeTime,
  revokeDevice,
  type Device,
  type RevokeFailure,
} from '../lib/machines';

const card = 'flex flex-col gap-3 rounded-2xl border border-line bg-surface p-5';
const rowButton =
  'inline-flex h-11 items-center justify-center rounded-[10px] border-0 bg-transparent px-3.5 text-sm font-bold text-fg2 hover:text-fg disabled:opacity-45 md:h-9';

function DeviceRow({ device, onRemoved }: { device: Device; onRemoved: (id: string) => void }) {
  const queryClient = useQueryClient();
  const [step, setStep] = useState<'idle' | 'confirm' | 'busy'>('idle');
  const [failure, setFailure] = useState<RevokeFailure | null>(null);
  const revokeRef = useRef<HTMLButtonElement>(null);
  const cancelRef = useRef<HTMLButtonElement>(null);
  const returnFocus = useRef(false);

  // Focus follows the step: Cancel (the safe choice) when asking, Revoke again after backing out.
  useEffect(() => {
    if (step === 'confirm') cancelRef.current?.focus();
    else if (step === 'idle' && returnFocus.current) {
      returnFocus.current = false;
      revokeRef.current?.focus();
    }
  }, [step]);

  const seen = relativeTime(device.last_used_at);
  const details = `${osLabel(device.os)} · dojo ${device.cli_version} · ${seen ? `last seen ${seen}` : 'not seen yet'}`;

  function cancel() {
    returnFocus.current = true;
    setStep('idle');
  }

  async function confirm() {
    setStep('busy');
    setFailure(null);
    const result = await revokeDevice(device.id);
    if (result.ok) {
      onRemoved(device.id);
      return;
    }
    if (result.reason === 'signed_out') {
      queryClient.setQueryData(ME_KEY, null);
      return;
    }
    returnFocus.current = true;
    setFailure(result.reason);
    setStep('idle');
  }

  return (
    <li className="flex flex-col gap-2 border-t border-line py-3.5">
      <div className="flex flex-wrap items-center justify-between gap-x-3 gap-y-2">
        <div className="flex min-w-0 flex-col gap-0.5">
          <span className="font-mono text-sm font-semibold break-all">{device.device_name}</span>
          <span className="text-[13px] text-muted">{details}</span>
        </div>
        {step === 'idle' ? (
          <button
            ref={revokeRef}
            type="button"
            aria-label={`Revoke ${device.device_name}`}
            onClick={() => {
              setFailure(null);
              setStep('confirm');
            }}
            className={rowButton}
          >
            Revoke
          </button>
        ) : (
          <div
            role="group"
            aria-label={`Revoke ${device.device_name}?`}
            onKeyDown={(e) => {
              if (e.key === 'Escape' && step !== 'busy') cancel();
            }}
            className="flex items-center gap-1"
          >
            <span aria-hidden="true" className="text-sm font-bold text-fg">
              Revoke?
            </span>
            <button
              type="button"
              aria-label={`Yes, revoke ${device.device_name}`}
              disabled={step === 'busy'}
              aria-busy={step === 'busy'}
              onClick={() => void confirm()}
              className="inline-flex h-11 items-center justify-center rounded-[10px] border-0 bg-bad px-3.5 text-sm font-bold text-white disabled:opacity-45 md:h-9"
            >
              {step === 'busy' ? 'Revoking...' : 'Yes'}
            </button>
            <button
              ref={cancelRef}
              type="button"
              aria-label={`Cancel revoking ${device.device_name}`}
              disabled={step === 'busy'}
              onClick={cancel}
              className={rowButton}
            >
              Cancel
            </button>
          </div>
        )}
      </div>
      <p role="status" aria-live="polite" className="m-0 text-[13px] font-semibold text-bad empty:hidden">
        {failure ? REVOKE_MESSAGES[failure] : ''}
      </p>
    </li>
  );
}

function MachinesCard({ headingRef }: { headingRef: RefObject<HTMLHeadingElement | null> }) {
  const queryClient = useQueryClient();
  const titleId = useId();
  const devices = useQuery({ queryKey: DEVICES_KEY, queryFn: fetchDevices, retry: false });
  const reason = devices.error instanceof DevicesError ? devices.error.reason : 'failed';

  useEffect(() => {
    if (devices.isError && reason === 'signed_out') queryClient.setQueryData(ME_KEY, null);
  }, [devices.isError, reason, queryClient]);

  function onRemoved(id: string) {
    // The row's button is about to disappear: park focus on the card title first.
    headingRef.current?.focus();
    queryClient.setQueryData<Device[]>(DEVICES_KEY, (cur) => cur?.filter((d) => d.id !== id));
    void queryClient.invalidateQueries({ queryKey: DEVICES_KEY });
  }

  let body;
  if (devices.isPending) {
    body = (
      <p role="status" className="m-0 border-t border-line pt-3.5 text-sm text-muted">
        Loading...
      </p>
    );
  } else if (devices.isError) {
    body = (
      <div role="alert" className="flex flex-wrap items-center justify-between gap-2.5 border-t border-line pt-3.5">
        <p className="m-0 text-sm text-fg2">{DEVICES_MESSAGES[reason]}</p>
        {reason === 'failed' && (
          <button type="button" onClick={() => void devices.refetch()} className={rowButton}>
            Retry
          </button>
        )}
      </div>
    );
  } else if (devices.data.length === 0) {
    body = (
      <p className="m-0 border-t border-line pt-3.5 text-sm leading-[1.55] text-fg2">
        No machines yet. Run <code className="font-mono text-[13px]">dojo login</code> on a machine to link it to your account.
      </p>
    );
  } else {
    body = (
      <ul className="m-0 flex list-none flex-col p-0" aria-labelledby={titleId}>
        {devices.data.map((d) => (
          <DeviceRow key={d.id} device={d} onRemoved={onRemoved} />
        ))}
      </ul>
    );
  }

  return (
    <section className={card} aria-labelledby={titleId}>
      <h2 id={titleId} ref={headingRef} tabIndex={-1} className="m-0 text-base font-extrabold outline-none">
        Machines linked to your account
      </h2>
      <p className="m-0 text-sm leading-[1.55] text-fg2">Each one ran dojo login. Revoke any you do not use any more.</p>
      {body}
    </section>
  );
}

export function Machines({ me }: { me: Me }) {
  const headingRef = useRef<HTMLHeadingElement>(null);
  return (
    <SettingsLayout me={me}>
      <MachinesCard headingRef={headingRef} />
    </SettingsLayout>
  );
}
