import { StrictMode } from 'react';
import { createRoot } from 'react-dom/client';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { createBrowserRouter, RouterProvider } from 'react-router';
import { routes } from './routes';
import { registerServiceWorker } from './lib/pwa';
import './styles.css';

const queryClient = new QueryClient();
const router = createBrowserRouter(routes, { basename: '/app' });

const root = document.getElementById('root');
if (!root) throw new Error('missing #root');

createRoot(root).render(
  <StrictMode>
    <QueryClientProvider client={queryClient}>
      <RouterProvider router={router} />
    </QueryClientProvider>
  </StrictMode>,
);

window.addEventListener('load', () => {
  void registerServiceWorker(import.meta.env.PROD, 'serviceWorker' in navigator ? navigator.serviceWorker : undefined);
});
