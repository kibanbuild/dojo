import { Link } from 'react-router';
import { OnboardingFrame } from '../components/OnboardingFrame';

export type PlaceholderStep = 'o5';

// Frame position (O1 sign in is step 2) and heading of each step that is not built yet.
const STEPS: Record<PlaceholderStep, { step: number; title: string }> = {
  o5: { step: 6, title: 'Your first ticket' },
};

/** O4 and O5 until their pages land: the frame and the step, so the router already has somewhere to send a learner. */
export function StartStep({ id }: { id: PlaceholderStep }) {
  const { step, title } = STEPS[id];
  return (
    <OnboardingFrame step={step}>
      <h1 className="m-0 text-[30px] leading-[1.06] font-extrabold tracking-[-1.5px] md:text-[44px]">{title}</h1>
      <p className="m-0 text-[15px] leading-[1.55] text-fg2">This step is coming to the app soon.</p>
      <Link to="/settings" className="self-start text-[15px] font-bold text-accentink">
        Open settings
      </Link>
    </OnboardingFrame>
  );
}
