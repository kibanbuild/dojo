export async function registerServiceWorker(prod: boolean, container: ServiceWorkerContainer | undefined): Promise<void> {
  if (!prod || !container) return;
  try {
    await container.register('/app/sw.js', { scope: '/app/' });
  } catch {
    // The app works without a worker; only offline support is lost.
  }
}
