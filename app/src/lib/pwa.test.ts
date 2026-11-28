import { describe, expect, it, vi } from 'vitest';
import { registerServiceWorker } from './pwa';

describe('registerServiceWorker', () => {
  it('registers /app/sw.js with scope /app/ in production', async () => {
    const register = vi.fn(async () => ({}));
    await registerServiceWorker(true, { register } as unknown as ServiceWorkerContainer);
    expect(register).toHaveBeenCalledWith('/app/sw.js', { scope: '/app/' });
  });

  it('does nothing outside production', async () => {
    const register = vi.fn();
    await registerServiceWorker(false, { register } as unknown as ServiceWorkerContainer);
    expect(register).not.toHaveBeenCalled();
  });

  it('does nothing without service worker support', async () => {
    await expect(registerServiceWorker(true, undefined)).resolves.toBeUndefined();
  });

  it('swallows a failed registration', async () => {
    const register = vi.fn(async () => {
      throw new Error('denied');
    });
    await expect(registerServiceWorker(true, { register } as unknown as ServiceWorkerContainer)).resolves.toBeUndefined();
  });
});
